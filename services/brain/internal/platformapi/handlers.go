package platformapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// defaultQuoteUnitCredits is the fallback used ONLY if unit_price_credits is unreadable (should
// never happen post-sender_0021 — the key is seeded with this exact value). Not itself a tuning
// dial: the real, live-retunable value is read fresh from sender.config on every request via
// readUnitPriceCredits (Phase 9.5 S10, §2.4) — config-not-code, no redeploy needed for a price
// change to take effect.
const defaultQuoteUnitCredits = 1

// uuidRe validates a canonical hyphenated UUID (job_item_id / job_id inputs).
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// --- POST /v1/quote — recipient count → cost (trivial pass-through, §6.1) ---

type quoteReq struct {
	RecipientCount *int `json:"recipient_count"`
}

func (s *Server) handleQuote(w http.ResponseWriter, r *http.Request) {
	var req quoteReq
	if e := decodeStrict(r.Body, &req); e != nil {
		s.fail(w, r, e)
		return
	}
	if req.RecipientCount == nil || *req.RecipientCount < 0 {
		s.fail(w, r, newErr(codeValidationFailed, "recipient_count_required").
			withDetails(fieldDetail{Field: "recipient_count", Issue: issueRequired}))
		return
	}
	ctx, cancel := dbCtx(r.Context())
	defer cancel()
	unit := s.readUnitPriceCredits(ctx)
	writeJSON(w, http.StatusOK, map[string]any{
		"recipient_count": *req.RecipientCount,
		"cost":            *req.RecipientCount * unit,
		"currency":        "credits",
	})
}

// --- POST /v1/run — accept a job, return immediately (async kickoff, §6.2) ---

type runReq struct {
	JobItemID string `json:"job_item_id"`
	UserID    string `json:"user_id"` // tenant selector (OD-25 option b) — the owning tenant
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	var req runReq
	if e := decodeStrict(r.Body, &req); e != nil {
		s.fail(w, r, e)
		return
	}
	if !uuidRe.MatchString(req.JobItemID) {
		s.fail(w, r, newErr(codeValidationFailed, "job_item_id_required").
			withDetails(fieldDetail{Field: "job_item_id", Issue: issueRequired}))
		return
	}
	// Tenant selector is REQUIRED and must be a UUID (OD-25 option b — module-enforced tenancy).
	if !uuidRe.MatchString(req.UserID) {
		s.fail(w, r, newErr(codeValidationFailed, "user_id_required").
			withDetails(fieldDetail{Field: "user_id", Issue: issueRequired}))
		return
	}

	ctx, cancel := dbCtx(r.Context())
	defer cancel()

	// Create one sender.jobs row (state=queued, genesis event via the actor='system' GUC), reading
	// the frozen recipient/subject/body/reply_to from public.job_items (SELECT-only, Phase 1),
	// SCOPED to the calling tenant: the job_item must belong to req.UserID (else 404-style
	// not-found — no cross-tenant existence disclosure). The job_item_id UNIQUE constraint makes a
	// duplicate run idempotent (§6.2).
	jobID, existing, e := s.createJob(ctx, req.JobItemID, req.UserID)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	if existing {
		// Duplicate run for the same job_item_id: return the existing job + its current state
		// (treated as "already accepted", never a raw 23505). 200 with the existing job.
		state, _ := s.jobStateByItem(ctx, req.JobItemID, req.UserID)
		writeJSON(w, http.StatusOK, map[string]any{"job_id": jobID, "state": state})
		return
	}

	// Async kickoff: nudge the dispatcher WITHOUT blocking on the send (§6.2). run only INSERTs.
	if s.kick != nil {
		s.kick()
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": jobID, "state": "queued"})
}

// --- GET /v1/report — reflect the job's state (per-tenant, clean state only, §6.3) ---

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Query().Get("job_id")
	jobItemID := r.URL.Query().Get("job_item_id")
	userID := r.URL.Query().Get("user_id")
	if jobID == "" && jobItemID == "" {
		s.fail(w, r, newErr(codeValidationFailed, "job_id_or_job_item_id_required").
			withDetails(fieldDetail{Field: "job_id", Issue: issueRequired}))
		return
	}
	// Tenant selector is REQUIRED and must be a UUID (OD-25 option b — module-enforced tenancy).
	if !uuidRe.MatchString(userID) {
		s.fail(w, r, newErr(codeValidationFailed, "user_id_required").
			withDetails(fieldDetail{Field: "user_id", Issue: issueRequired}))
		return
	}
	if jobID != "" && !uuidRe.MatchString(jobID) {
		s.fail(w, r, newErr(codeValidationFailed, "job_id_malformed").
			withDetails(fieldDetail{Field: "job_id", Issue: issueMalformed}))
		return
	}
	if jobItemID != "" && !uuidRe.MatchString(jobItemID) {
		s.fail(w, r, newErr(codeValidationFailed, "job_item_id_malformed").
			withDetails(fieldDetail{Field: "job_item_id", Issue: issueMalformed}))
		return
	}

	ctx, cancel := dbCtx(r.Context())
	defer cancel()

	// SCOPED to the calling tenant (userID). A job that exists but belongs to another tenant
	// matches 0 rows and returns the SAME not_found as a non-existent job — no existence
	// disclosure across tenants (404-style, never 403 — OD-25).
	rep, ok, err := s.readReport(ctx, jobID, jobItemID, userID)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "report_query_failed"))
		return
	}
	if !ok {
		s.fail(w, r, newErr(codeNotFound, "job_not_found").
			withDetails(fieldDetail{Field: "job_id", Issue: issueNotFound}))
		return
	}
	// Clean external state ONLY (§6.3): job_id, state, provider_message_id (when sent),
	// completed_at, attempt_count. NEVER the SMTP text, error_reason, credential, or internal reason.
	out := map[string]any{
		"job_id":        rep.jobID,
		"state":         rep.state,
		"attempt_count": rep.attemptCount,
	}
	if rep.providerMessageID != nil {
		out["provider_message_id"] = *rep.providerMessageID
	}
	if rep.completedAt != nil {
		out["completed_at"] = rep.completedAt.UTC()
	}
	writeJSON(w, http.StatusOK, out)
}

// --- GET /v1/describe — minimal tiers/pricing stub (§6.4) ---

func (s *Server) handleDescribe(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r.Context())
	defer cancel()
	unit := s.readUnitPriceCredits(ctx)
	writeJSON(w, http.StatusOK, map[string]any{
		"module":       "sender",
		"phase":        3,
		"capabilities": []string{"single_send"},
		"pricing": map[string]any{
			"tier":     "flat",
			"unit":     unit,
			"currency": "credits",
			"note":     "Phase 3 flat pricing stub; real tiers land in Phase 9.",
		},
	})
}

// readUnitPriceCredits reads unit_price_credits fresh from sender.config (Phase 9.5 S10, §2.4) —
// live-retunable, no redeploy. platformapi does NOT import internal/dispatch's LoadConfig
// (structural package isolation, OD-7's precedent — mirrors internal/adminapi's own local config
// readers), so this is a minimal, scoped-to-one-key local reader. Falls back to the seeded default
// on any read/parse failure rather than failing the request — quote/describe are read-mostly,
// low-stakes endpoints and a transient config-read blip must not 500 every quote in flight.
func (s *Server) readUnitPriceCredits(ctx context.Context) int {
	var raw []byte
	if err := s.db.QueryRow(ctx, `SELECT value FROM sender.config WHERE key = 'unit_price_credits'`).Scan(&raw); err != nil {
		return defaultQuoteUnitCredits
	}
	var wrap struct {
		V *int `json:"v"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil || wrap.V == nil {
		return defaultQuoteUnitCredits
	}
	return *wrap.V
}

// --- helpers ---

// decodeStrict reads a JSON body with unknown fields rejected. An empty body is allowed for
// endpoints with all-optional fields (the caller validates required fields itself).
func decodeStrict(body io.Reader, dst any) *apiError {
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil // empty body; required-field checks run next
		}
		return newErr(codeValidationFailed, "malformed_json: "+sanitizeDecodeErr(err))
	}
	return nil
}

// sanitizeDecodeErr keeps a short internal hint from a decode error WITHOUT echoing body content.
func sanitizeDecodeErr(err error) string {
	msg := err.Error()
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	if len(msg) > 120 {
		msg = msg[:120]
	}
	return msg
}

// pgCode extracts the SQLSTATE from a pg error (never surfaced to the client).
func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// isNoRows reports pgx.ErrNoRows.
func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
