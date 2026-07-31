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
// per-job shape the single-job report already returns (§6.3), never SMTP/error/credential detail.
type campaignItem struct {
	JobItemID         string  `json:"job_item_id"`
	State             string  `json:"state"`
	ProviderMessageID *string `json:"provider_message_id"`
	CompletedAt       *string `json:"completed_at"`
	AttemptCount      int     `json:"attempt_count"`
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
		"campaign_id":     campaignID,
		"dispatch_paused": dispatchPaused,
		"totals":          totals,
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
// G2 STUB (M3-P2-OD-12, extending the health-endpoint ruling to platformapi): waiting_for_capacity
// pre-G2 resolved the user's visible POOLS (dedicated owned + shared) and checked eligibility
// across them. byosmtp.pools is dropped by this phase's own migrations, so that query cannot
// survive unmodified — this is a structurally-correct interim ("does this user have any account at
// all"), matching internal/adminapi/health_store.go's queryWaitingForCapacity stub exactly (same
// reasoning, same phase split: G2 stub, G3 real accounts_exhausted rewrite per M3-P2-OD-3).
func (s *Server) campaignTotals(ctx context.Context, campaignID, userID string) (campaignTotals, error) {
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
        SELECT 1 FROM byosmtp.accounts a WHERE a.user_id = $2::uuid
    )) AS waiting_for_capacity
FROM scoped`

	var totals campaignTotals
	var pendingTotal int
	if err := s.db.QueryRow(ctx, q, campaignID, userID).Scan(
		&totals.Total, &totals.Sent, &totals.Bounced, &totals.Failed, &totals.InFlight,
		&pendingTotal, &totals.WaitingForCapacity); err != nil {
		return campaignTotals{}, err
	}
	// pending = queued jobs that are NOT currently waiting_for_capacity (i.e. dispatchable now).
	totals.Pending = pendingTotal - totals.WaitingForCapacity
	return totals, nil
}

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
	q := `SELECT j.job_item_id::text, j.state::text, j.provider_message_id, j.completed_at, j.attempt_count,
	             j.created_at, j.id::text
	      FROM byosmtp.jobs j ` + where + `
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
		if err := rows.Scan(&it.JobItemID, &it.State, &it.ProviderMessageID, &completedAt, &it.AttemptCount,
			&lastCreatedAt, &lastID); err != nil {
			return nil, nil, err
		}
		if completedAt != nil {
			s := completedAt.UTC().Format("2006-01-02T15:04:05Z")
			it.CompletedAt = &s
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
