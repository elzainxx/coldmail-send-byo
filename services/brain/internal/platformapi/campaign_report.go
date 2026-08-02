package platformapi

// campaign_report.go — Phase 9 Chunk 4 (B8, §2 of phase-9-contract-and-doc-completions.md): the
// campaign-scoped GET /v1/report?campaign_id= response. Derives ENTIRELY from byosmtp.jobs state
// counts + existing per-job fields — no new state, no schema change. report remains a read/poll
// of existing data (§2 ruling).
//
// The three-way distinction a UI must render without reading internal state-machine docs (§2.3):
// waiting_for_capacity (still pending, no eligible account right now — NEVER shown as failed),
// failed (terminal, true pool exhaustion — refundable, platform's policy), bounced (terminal,
// recipient-permanent — billable). All three, plus plain pending, are surfaced as aggregate COUNTS
// in totals — the per-item state stays exactly the byosmtp.job_state enum value (queued/assigned/
// sending/sent/failed/bounced); waiting_for_capacity is an AGGREGATE distinction, not a new
// per-job state (§2.3).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// campaignTotals mirrors §2.2's "totals" object.
type campaignTotals struct {
	Total              int `json:"total"`
	Sent               int `json:"sent"`
	Bounced            int `json:"bounced"`
	Failed             int `json:"failed"`
	InFlight           int `json:"in_flight"`
	Pending            int `json:"pending"`
	WaitingForCapacity int `json:"waiting_for_capacity"`
}

// campaignItem mirrors one entry of §2.2's "items.results" array — the SAME clean external
// per-job shape the single-job report already returns (§6.3), WIDENED by Phase 7 §1.1
// (SMTPResponse/ErrorClass/FailedAt — never last_error_message, never error_reason, never a
// credential) and §2 (AccountID/AccountUsername — the per-account attribution delta). Explicit
// nil (not an omitted key) on all five widened fields, matching this struct's own existing
// null-handling convention for ProviderMessageID/CompletedAt — the single-job report's map-based
// omit-the-key shape is a SEPARATE, deliberately inconsistent contract (M3-P7-OD-4), not
// something this file should silently "fix" to match.
type campaignItem struct {
	JobItemID         string  `json:"job_item_id"`
	State             string  `json:"state"`
	ProviderMessageID *string `json:"provider_message_id"`
	CompletedAt       *string `json:"completed_at"`
	AttemptCount      int     `json:"attempt_count"`
	AccountID         *string `json:"account_id"`
	AccountUsername   *string `json:"account_username"`
	SMTPResponse      *string `json:"smtp_response"`
	ErrorClass        *string `json:"error_class"`
	FailedAt          *string `json:"failed_at"`
}

// accountTotals mirrors one entry of §2.1's `totals_by_account[]` — the aggregate that answers
// "3 of my 5 accounts are failing" without forcing every client to re-implement the same
// per-item GROUP BY. AccountID/AccountUsername are never null in this slice: a row exists ONLY
// for an account that has been assigned at least one job of this campaign (§2.2's GROUP BY), so
// there is no "unassigned" bucket to represent — unassigned/queued jobs are already counted in
// campaignTotals.Pending/WaitingForCapacity.
type accountTotals struct {
	AccountID       string `json:"account_id"`
	AccountUsername string `json:"account_username"`
	Sent            int    `json:"sent"`
	Bounced         int    `json:"bounced"`
	Failed          int    `json:"failed"`
	InFlight        int    `json:"in_flight"`
}

// handleCampaignReport serves GET /v1/report?campaign_id=<id> (B8, §2.1-§2.4). Scoped to the
// calling tenant exactly like the single-job report (OD-25): a campaign_id that exists but belongs
// to another tenant, or does not exist at all, matches zero rows and returns a clean EMPTY
// campaign report (total:0) rather than 404 — a campaign_id is not a single resource identity the
// way job_id/job_item_id are (the platform mints it, the module never allocates one), so "zero
// jobs under this campaign_id for this tenant" is the correct, honest answer whether the id is
// foreign, mistyped, or simply not-yet-run — never a cross-tenant existence signal either way,
// since the WHERE clause scopes to user_id in every branch.
func (s *Server) handleCampaignReport(w http.ResponseWriter, r *http.Request, campaignID, userID string) {
	if !uuidRe.MatchString(campaignID) {
		s.fail(w, r, newErr(codeValidationFailed, "campaign_id_malformed").
			withDetails(fieldDetail{Field: "campaign_id", Issue: issueMalformed}))
		return
	}
	cur, cerr := parseReportCursor(r)
	if cerr != nil {
		s.fail(w, r, cerr)
		return
	}
	pageSize := parseReportPageSize(r)

	ctx, cancel := dbCtx(r.Context())
	defer cancel()

	totals, err := s.campaignTotals(ctx, campaignID, userID)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "campaign_report_totals_failed"))
		return
	}
	totalsByAccount, err := s.campaignAccountTotals(ctx, campaignID, userID)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "campaign_report_account_totals_failed"))
		return
	}
	items, nextCursor, err := s.campaignItems(ctx, campaignID, userID, cur, pageSize)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "campaign_report_items_failed"))
		return
	}
	dispatchPaused, err := s.readDispatchPaused(ctx)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "campaign_report_dispatch_state_failed"))
		return
	}

	terminal := totals.Sent + totals.Bounced + totals.Failed
	fractionComplete := 0.0
	if totals.Total > 0 {
		fractionComplete = float64(terminal) / float64(totals.Total)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"campaign_id":       campaignID,
		"dispatch_paused":   dispatchPaused,
		"totals":            totals,
		"totals_by_account": totalsByAccount,
		"progress": map[string]any{
			"terminal":          terminal,
			"fraction_complete": fractionComplete,
			"is_complete":       totals.Total > 0 && terminal == totals.Total,
		},
		"items": map[string]any{
			"results":     items,
			"next_cursor": nextCursor,
			"page_size":   pageSize,
		},
	})
}

// campaignTotals computes §2.2's totals object in ONE query, tenant-scoped.
//
// G3 REAL REWRITE (M3-P2-OD-12): waiting_for_capacity uses the SAME account-eligibility predicate
// internal/dispatch/eligibility.go's accountBaseEligibilityFilter + userHasEligibleQueryTmpl
// define for the real dispatch decision — quarantined/disabled/deleted/credential-unverified/
// from-misaligned accounts are structurally excluded, and the daily-reset/throttle cycle logic
// (which accounts are "still cooling down" vs. "eligible again today") is reproduced exactly, not
// approximated as "has any account row." platformapi does not import internal/dispatch (package
// isolation, OD-7's precedent), so this is its own minimal copy — cross-reference this comment if
// eligibility.go's predicate ever gains a term, since this copy does not follow automatically.
// Byte-identical in substance to internal/adminapi/health_store.go's own copy of the same
// predicate (that surface's own package-isolation copy, for the identical reason) — the two
// admin/platform surfaces must never disagree about what "eligible" means.
func (s *Server) campaignTotals(ctx context.Context, campaignID, userID string) (campaignTotals, error) {
	tz, ok, err := s.readConfigString(ctx, "daily_reset_timezone")
	if err != nil {
		return campaignTotals{}, err
	}
	if !ok {
		tz = "UTC"
	}
	throttleSec, ok, err := s.readConfigInt(ctx, "throttle_pause_seconds")
	if err != nil {
		return campaignTotals{}, err
	}
	if !ok {
		throttleSec = 900
	}
	hour, ok, err := s.readConfigInt(ctx, "daily_reset_hour")
	if err != nil {
		return campaignTotals{}, err
	}
	if !ok {
		hour = 0
	}

	const q = `
WITH scoped AS (
    SELECT j.id, j.state, j.user_id
    FROM byosmtp.jobs j
    WHERE j.campaign_id = $1 AND j.user_id = $2
)
SELECT
    COUNT(*) AS total,
    COUNT(*) FILTER (WHERE state = 'sent') AS sent,
    COUNT(*) FILTER (WHERE state = 'bounced') AS bounced,
    COUNT(*) FILTER (WHERE state = 'failed') AS failed,
    COUNT(*) FILTER (WHERE state IN ('assigned', 'sending')) AS in_flight,
    COUNT(*) FILTER (WHERE state = 'queued') AS pending_total,
    COUNT(*) FILTER (WHERE state = 'queued' AND NOT EXISTS (
        ` + campaignEligibleAccountsSubquery + `
    )) AS waiting_for_capacity
FROM scoped`

	var totals campaignTotals
	var pendingTotal int
	if err := s.db.QueryRow(ctx, q, campaignID, userID, tz, throttleSec, hour).Scan(
		&totals.Total, &totals.Sent, &totals.Bounced, &totals.Failed, &totals.InFlight,
		&pendingTotal, &totals.WaitingForCapacity); err != nil {
		return campaignTotals{}, err
	}
	// pending = queued jobs that are NOT currently waiting_for_capacity (i.e. dispatchable now).
	totals.Pending = pendingTotal - totals.WaitingForCapacity
	return totals, nil
}

// campaignAccountTotals computes §2.1's `totals_by_account[]` — a SECOND `GROUP BY
// assigned_account_id` query over the same jobs rows campaignTotals scans (§2.2: "not a rework
// of the COUNT(*) FILTER totals — keeping them separate means the existing totals cannot regress
// while the new aggregate is added"). INNER JOIN here, deliberately unlike campaignItems' LEFT
// JOIN: a row in this slice exists only for an account that was actually assigned at least one
// job of this campaign, so an unassigned/never-dispatched job (assigned_account_id IS NULL)
// correctly contributes to no bucket rather than a spurious "account: null" entry.
func (s *Server) campaignAccountTotals(ctx context.Context, campaignID, userID string) ([]accountTotals, error) {
	const q = `
SELECT a.id::text, a.smtp_username,
       COUNT(*) FILTER (WHERE j.state = 'sent') AS sent,
       COUNT(*) FILTER (WHERE j.state = 'bounced') AS bounced,
       COUNT(*) FILTER (WHERE j.state = 'failed') AS failed,
       COUNT(*) FILTER (WHERE j.state IN ('assigned', 'sending')) AS in_flight
FROM byosmtp.jobs j
JOIN byosmtp.accounts a ON a.id = j.assigned_account_id
WHERE j.campaign_id = $1 AND j.user_id = $2
GROUP BY a.id, a.smtp_username
ORDER BY a.smtp_username ASC`

	rows, err := s.db.Query(ctx, q, campaignID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []accountTotals{}
	for rows.Next() {
		var at accountTotals
		if err := rows.Scan(&at.AccountID, &at.AccountUsername, &at.Sent, &at.Bounced, &at.Failed, &at.InFlight); err != nil {
			return nil, err
		}
		out = append(out, at)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// campaignEligibleAccountsSubquery is the real §4.1 eligibility predicate (Phase 4's
// accountBaseEligibilityFilter, consolidated M3-P4-OD-3, plus the daily-reset/throttle cycle logic
// from userHasEligibleQueryTmpl), scoped to the enclosing query's user_id ($2) and referencing the
// OUTER query's own parameters: $2=user_id, $3=daily_reset_timezone, $4=throttle_pause_seconds,
// $5=daily_reset_hour (binds in the same order internal/dispatch/eligibility.go's
// userHasEligibleQueryTmpl uses — tz, then hour, is $1/$4 there; here it is $3/$5, since $1/$2 are
// already taken by campaign_id/user_id). Nested as a standalone EXISTS (no CTE to join against,
// unlike the pre-G2 pool version's user_pools CTE) since accounts are looked up directly by
// user_id now — there is no intermediate pool row to resolve first.
const campaignEligibleAccountsSubquery = `
    SELECT 1
    FROM byosmtp.accounts a
    WHERE a.user_id = $2::uuid
      AND a.quarantined = FALSE
      AND a.disabled = FALSE
      AND a.deleted_at IS NULL
      AND a.credential_verification_state = 'verified'
      AND (a.from_verification_state = 'aligned'
           OR (a.from_verification_state = 'unaligned_warned'
               AND a.from_warning_acknowledged_at IS NOT NULL
               AND a.from_warning_acknowledged_for = a.from_verdict_fingerprint))
      AND (
            (a.daily_reset_day < ((NOW() - INTERVAL '1 hour' * $5) AT TIME ZONE $3)::date
                AND (a.paused = FALSE
                     OR a.pause_reason = 'daily_limit'
                     OR (a.pause_reason = 'throttle' AND a.throttle_paused_at IS NOT NULL
                         AND NOW() >= a.throttle_paused_at + INTERVAL '1 second' * $4)))
            OR
            (a.daily_reset_day = ((NOW() - INTERVAL '1 hour' * $5) AT TIME ZONE $3)::date
                AND (a.paused = FALSE
                     OR (a.pause_reason = 'throttle' AND a.throttle_paused_at IS NOT NULL
                         AND NOW() >= a.throttle_paused_at + INTERVAL '1 second' * $4))
                AND a.daily_sent_count < a.daily_limit)
          )`

// campaignItems returns one page of per-job detail (§2.2 "items"), cursor-paginated on
// (created_at, id) — the same keyset pattern the admin surface uses (§5.1 pagination
// conventions), tenant-scoped.
func (s *Server) campaignItems(ctx context.Context, campaignID, userID string, cur *reportCursor, pageSize int) ([]campaignItem, *string, error) {
	args := []any{campaignID, userID}
	where := "WHERE j.campaign_id = $1 AND j.user_id = $2"
	if cur != nil {
		args = append(args, cur.CreatedAt, cur.ID)
		where += " AND (j.created_at, j.id) > ($3::timestamptz, $4::uuid)"
	}
	args = append(args, pageSize+1)
	limitParam := len(args)
	// The failure-detail columns provider_text.go's accessor governs are read HERE, via
	// providerFailureSelectCols — this function is P2's half of §1.5's "single named accessor"
	// (store.go's readReport is P1's half). The internal reason column is never named in this
	// query, by construction. §2.2's LEFT JOIN (never INNER — an unassigned job must still appear
	// in results[], or its count would silently disagree with totals.total).
	q := `SELECT j.job_item_id::text, j.state::text, j.provider_message_id, j.completed_at, j.attempt_count,
	             j.assigned_account_id, a.smtp_username,
	             ` + providerFailureSelectCols + `,
	             j.created_at, j.id::text
	      FROM byosmtp.jobs j LEFT JOIN byosmtp.accounts a ON a.id = j.assigned_account_id
	      ` + where + `
	      ORDER BY j.created_at ASC, j.id ASC
	      LIMIT $` + strconv.Itoa(limitParam)

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	items := []campaignItem{}
	var lastCreatedAt time.Time
	var lastID string
	for rows.Next() {
		var it campaignItem
		var completedAt *time.Time
		var smtpResponse, errorClass *string
		if err := rows.Scan(&it.JobItemID, &it.State, &it.ProviderMessageID, &completedAt, &it.AttemptCount,
			&it.AccountID, &it.AccountUsername,
			&smtpResponse, &errorClass,
			&lastCreatedAt, &lastID); err != nil {
			return nil, nil, err
		}
		if completedAt != nil {
			s := completedAt.UTC().Format("2006-01-02T15:04:05Z")
			it.CompletedAt = &s
		}
		detail := readProviderFailureDetail(it.State, smtpResponse, errorClass, completedAt)
		it.SMTPResponse = detail.SMTPResponse
		it.ErrorClass = detail.ErrorClass
		if detail.FailedAt != nil {
			s := detail.FailedAt.UTC().Format("2006-01-02T15:04:05Z")
			it.FailedAt = &s
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	var nextCursor *string
	if len(items) > pageSize {
		items = items[:pageSize]
		c := encodeReportCursor(lastCreatedAt.Format(time.RFC3339Nano), lastID)
		nextCursor = &c
	}
	return items, nextCursor, nil
}

// readDispatchPaused reads the LIVE dispatch_enabled config (B1) and reports its negation —
// "dispatch_paused" (§2.2) is the operator-visible framing a UI renders; the underlying dial's
// name/semantics are unchanged (CLAUDE.md Rule 1 — no shadow constant, this is a fresh read).
func (s *Server) readDispatchPaused(ctx context.Context) (bool, error) {
	var raw []byte
	if err := s.db.QueryRow(ctx, `SELECT value FROM byosmtp.config WHERE key = 'dispatch_enabled'`).Scan(&raw); err != nil {
		return false, err
	}
	var wrap struct {
		V *bool `json:"v"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil || wrap.V == nil {
		return false, nil
	}
	return !*wrap.V, nil
}

// --- cursor helpers (campaign-report-local; mirror internal/adminapi/pagination.go's shape) ---

type reportCursor struct {
	CreatedAt string
	ID        string
}

func parseReportCursor(r *http.Request) (*reportCursor, *apiError) {
	raw := r.URL.Query().Get("cursor")
	if raw == "" {
		return nil, nil
	}
	dec, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, newErr(codeValidationFailed, "cursor_malformed").
			withDetails(fieldDetail{Field: "cursor", Issue: issueMalformed})
	}
	parts := strings.SplitN(string(dec), "|", 2)
	if len(parts) != 2 || parts[1] == "" {
		return nil, newErr(codeValidationFailed, "cursor_malformed").
			withDetails(fieldDetail{Field: "cursor", Issue: issueMalformed})
	}
	return &reportCursor{CreatedAt: parts[0], ID: parts[1]}, nil
}

func parseReportPageSize(r *http.Request) int {
	const defaultPageSize = 100
	const maxPageSize = 200
	q := r.URL.Query().Get("page_size")
	if q == "" {
		return defaultPageSize
	}
	n, err := strconv.Atoi(q)
	if err != nil || n <= 0 {
		return defaultPageSize
	}
	if n > maxPageSize {
		return maxPageSize
	}
	return n
}

func encodeReportCursor(createdAt, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt + "|" + id))
}
