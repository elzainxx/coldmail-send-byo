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
// never happen post-send_byo_0048 — the key is seeded with this exact value). Not itself a tuning
// dial: the real, live-retunable value is read fresh from send_byo.config on every request via
// readUnitPriceCredits (Phase 9.5 S10, §2.4) — config-not-code, no redeploy needed for a price
// change to take effect. Zero (M3-P7-OD-11, send_byo_0048): module 3 prices as a flat monthly
// subscription (module-3-build-plan.md §1) — there is no per-recipient charge. cost/
// unit_price_credits stay on the response (Rule 5 — never removed) but are retained purely for
// contract compatibility with the additive four-field shape module 2 established; they are not
// meaningful pricing under this module.
const defaultQuoteUnitCredits = 0

// defaultQuoteTier is the tier `quote` reports (Phase 9 §1.1's additive `tier` field). `quote`'s
// request carries only recipient_count — no job_item_id/user_id context — so it cannot resolve
// which pricing tier a campaign will actually route to. This is honest rather than a gap: both
// tiers charge the IDENTICAL unit_price_credits in V1, so the reported tier never affects the
// quoted cost either way. "shared" is reported as the base-campaign default.
const defaultQuoteTier = "shared"

// describeTiers is the SAME per-tier pricing shape quote's defaultQuoteTier draws from (Phase 9
// §2.1/§2.2) — the single source of truth describe and quote share so they can never diverge.
// unit is populated live per-request from readUnitPriceCredits; both tiers price IDENTICALLY in
// V1 (stated honestly rather than inventing a price differentiator). Zero by default
// (M3-P7-OD-11): module 3's real pricing is a flat monthly subscription, so unit_price_credits is
// retained here purely for Rule-5 contract compatibility with the field module 2 established.
func describeTiers(unit int) []map[string]any {
	return []map[string]any{
		{"id": "shared", "unit_price_credits": unit, "description": "Base SMTP sending."},
		{"id": "dedicated", "unit_price_credits": unit, "description": "User-owned SMTP account."},
	}
}

// uuidRe validates a canonical hyphenated UUID (job_item_id / job_id inputs).
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// --- POST /v1/quote — a FEASIBILITY check under module 3's flat-subscription pricing (Phase 7 §4) ---

type quoteReq struct {
	RecipientCount *int   `json:"recipient_count"`
	UserID         string `json:"user_id"` // M3-P7-OD-5: every §4.2 field is per-user; module 2's quote takes no tenant context
	// CampaignID (additive, Phase 3 close-out C2) names the campaign the recipients will join. Omitted, the
	// quote answers for a NEW campaign (the only case in which another campaign's active jobs block it, and
	// one that starts with zero jobs against max_campaign_size); given, jobs already in that campaign count
	// against the campaign cap and its own active jobs are not "another campaign".
	CampaignID string `json:"campaign_id"`
}

func (s *Server) handleQuote(w http.ResponseWriter, r *http.Request) {
	var req quoteReq
	if e := decodeStrict(r.Body, &req); e != nil {
		s.fail(w, r, e)
		return
	}
	// ABSENT and PRESENT-BUT-NEGATIVE are reported DISTINCTLY (issueRequired vs issueOutOfRange),
	// matching the run/report user_id checks below and the whole details[] vocabulary contract: a UI
	// building generic per-field messaging off details[].issue must be able to tell "you forgot this"
	// from "the number you sent is not allowed". These collapsed into one issueRequired until
	// 2026-07-28, so `{"recipient_count": -5}` told the caller a field it had plainly supplied was
	// missing (found walking Stage 7 of the local test guide). out_of_range, not malformed: a negative
	// integer decodes fine and is the right TYPE — it is the VALUE that is outside the allowed domain
	// (recipient_count >= 0), which is exactly adminapi's daily_limit/smtp_port precedent.
	if req.RecipientCount == nil {
		s.fail(w, r, newErr(codeValidationFailed, "recipient_count_required").
			withDetails(fieldDetail{Field: "recipient_count", Issue: issueRequired}))
		return
	}
	if *req.RecipientCount < 0 {
		s.fail(w, r, newErr(codeValidationFailed, "recipient_count_negative").
			withDetails(fieldDetail{Field: "recipient_count", Issue: issueOutOfRange}))
		return
	}
	// M3-P7-OD-5: user_id is a REQUIRED request field (additive to the request; a coordinated
	// platform change, not assumed) — every §4.2 field (eligible accounts, caps, projected
	// duration) is per-user, and module 2's own comment concedes it "cannot resolve which pool-kind
	// tier a campaign will actually route to" without one. Same validation shape as run/report's
	// user_id checks.
	if req.UserID == "" {
		s.fail(w, r, newErr(codeValidationFailed, "user_id_required").
			withDetails(fieldDetail{Field: "user_id", Issue: issueRequired}))
		return
	}
	if !uuidRe.MatchString(req.UserID) {
		s.fail(w, r, newErr(codeValidationFailed, "user_id_malformed").
			withDetails(fieldDetail{Field: "user_id", Issue: issueMalformed}))
		return
	}
	if req.CampaignID != "" && !uuidRe.MatchString(req.CampaignID) {
		s.fail(w, r, newErr(codeValidationFailed, "campaign_id_malformed").
			withDetails(fieldDetail{Field: "campaign_id", Issue: issueMalformed}))
		return
	}
	ctx, cancel := dbCtx(r.Context())
	defer cancel()

	feas, ferr := s.computeQuoteFeasibility(ctx, req.UserID, req.CampaignID, *req.RecipientCount)
	if ferr != nil {
		s.fail(w, r, ferr)
		return
	}

	unit := s.readUnitPriceCredits(ctx)
	out := map[string]any{
		"recipient_count": *req.RecipientCount,
		// cost/currency/unit_price_credits/tier are RETAINED (Rule 5 — never removed) but are
		// INERT under module 3's flat monthly subscription (M3-P7-OD-11, module-3-build-plan.md
		// §1: no per-send charge, no per-recipient price). unit_price_credits is seeded 0
		// (send_byo_0048), so cost is always 0 at the default; the fields exist purely for contract
		// compatibility with the additive shape module 2 established.
		"cost":               unit * (*req.RecipientCount),
		"currency":           "credits",
		"unit_price_credits": unit,
		"tier":               defaultQuoteTier,
	}
	for k, v := range quoteFeasibilityJSON(feas) {
		out[k] = v
	}
	writeJSON(w, http.StatusOK, out)
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
	// Empty vs malformed are reported distinctly (issueRequired vs issueMalformed), matching the
	// job_item_id/job_id/campaign_id checks elsewhere in this file — a UI building generic
	// per-field messaging off details[].issue must see one consistent vocabulary across every
	// UUID field on this surface (R2 finding).
	if req.UserID == "" {
		s.fail(w, r, newErr(codeValidationFailed, "user_id_required").
			withDetails(fieldDetail{Field: "user_id", Issue: issueRequired}))
		return
	}
	if !uuidRe.MatchString(req.UserID) {
		s.fail(w, r, newErr(codeValidationFailed, "user_id_malformed").
			withDetails(fieldDetail{Field: "user_id", Issue: issueMalformed}))
		return
	}

	ctx, cancel := dbCtx(r.Context())
	defer cancel()

	// Fix-plan item 12: the INTAKE rung. At `refuse_new` and `stopped` this module declines new work
	// with 503 `paused` rather than queueing a backlog it has been told not to work through. At
	// `serving` and `accept_and_defer` intake is unaffected — accept_and_defer's entire purpose is that
	// callers keep submitting while nothing is dispatched.
	//
	// PLACED HERE, AFTER VALIDATION AND BEFORE createJob, for two reasons. Before validation, a
	// malformed request would get 503 instead of 400 and a caller debugging its own bug would be told
	// the wrong thing. After createJob, the job would already exist and the refusal would be a lie.
	//
	// A READ FAILURE FAILS OPEN, DELIBERATELY, and this is the one judgement call in the gate. If the
	// config read errors we accept the job. The alternative — refuse on a transient blip — converts a
	// one-second DB hiccup into refused customer work, and it does so in the direction that LOSES
	// something: a refused submission may never be retried, whereas an accepted job just sits in
	// `queued` and is dispatched (or not) according to whatever the rung turns out to be. Accepting is
	// recoverable; refusing is not. The claim-path gates are the ones that must fail closed, and they
	// do: dispatch.LoadConfig returns a hard error and that tick claims nothing.
	if !s.intakeAllowed(ctx) {
		s.fail(w, r, newErr(codePaused, "run_refused_dispatch_rung_refuse_new_or_stopped"))
		return
	}

	// Create one send_byo.jobs row (state=queued, genesis event via the actor='system' GUC), reading
	// the frozen recipient/subject/body/reply_to from public.job_items (a SELECT here; the module's one write to that table is test-send's INSERT, send_byo_0037),
	// SCOPED to the calling tenant: the job_item must belong to req.UserID (else 404-style
	// not-found — no cross-tenant existence disclosure). The job_item_id UNIQUE constraint makes a
	// duplicate run idempotent (§6.2). An item the platform has marked `cancelled` is refused with 409
	// `job_item_cancelled` and no job row (ADR-0022), except that an item which ALREADY has a job is still the replay.
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
	campaignID := r.URL.Query().Get("campaign_id")
	userID := r.URL.Query().Get("user_id")
	if jobID == "" && jobItemID == "" && campaignID == "" {
		s.fail(w, r, newErr(codeValidationFailed, "job_id_or_job_item_id_or_campaign_id_required").
			withDetails(fieldDetail{Field: "job_id", Issue: issueRequired}))
		return
	}
	// Tenant selector is REQUIRED and must be a UUID (OD-25 option b — module-enforced tenancy).
	// Empty vs malformed reported distinctly, matching job_id/job_item_id below (R2 finding).
	if userID == "" {
		s.fail(w, r, newErr(codeValidationFailed, "user_id_required").
			withDetails(fieldDetail{Field: "user_id", Issue: issueRequired}))
		return
	}
	if !uuidRe.MatchString(userID) {
		s.fail(w, r, newErr(codeValidationFailed, "user_id_malformed").
			withDetails(fieldDetail{Field: "user_id", Issue: issueMalformed}))
		return
	}
	// campaign_id (B8, §2.1): the aggregate campaign-scoped response, a distinct mode from the
	// single-job lookup below. Mutually exclusive in practice (a caller polling a campaign has no
	// single job_id/job_item_id yet); if somehow both are present, campaign_id wins since it is the
	// more specific ask a UI makes when it already knows it wants the campaign aggregate.
	if campaignID != "" {
		s.handleCampaignReport(w, r, campaignID, userID)
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
		// Name the selector the CALLER actually sent. This branch is reached by either selector, and
		// hardcoding "job_id" pointed a job_item_id lookup at a field the caller never supplied —
		// misleading on the one code path (a polling integration keyed by job_item_id) where the
		// operator has no job_id to go check. The validation branches above already draw this
		// distinction, and store.go's run/quote not-founds already name job_item_id.
		// NO EXISTENCE DISCLOSURE (OD-25): this echoes back the caller's OWN choice of selector, not
		// anything about the row. Both selectors still return the SAME code, message, and status as a
		// wholly nonexistent job, so a cross-tenant probe learns nothing.
		field := "job_id"
		if jobID == "" {
			field = "job_item_id"
		}
		s.fail(w, r, newErr(codeNotFound, "job_not_found").
			withDetails(fieldDetail{Field: field, Issue: issueNotFound}))
		return
	}
	// Clean external state (§6.3), WIDENED by Phase 7 §1.1 (M3-P7-OD-1, conditional on M3-P7-OD-2's
	// scrub landing first — it has): job_id, state, provider_message_id (when sent), completed_at,
	// attempt_count, smtp_response/error_class/failed_at (only on a real failure/bounce — never
	// last_error_message, never error_reason, never a credential; §1.4/§1.5), and by Phase 7 §2
	// (M3-P7-OD-... attribution delta): account_id/account_username — omitted, not merely null,
	// when the job has no assigned account (never dispatched, or requeued and cleared; §2.1's
	// "nullable, and this is load-bearing"). account_id is a UUID string; account_username is the
	// human-readable field users actually act on.
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
	if rep.accountID != nil {
		out["account_id"] = *rep.accountID
	}
	if rep.accountUsername != nil {
		out["account_username"] = *rep.accountUsername
	}
	if rep.failureDetail.SMTPResponse != nil {
		// copy-ledger: report.smtp_response_passthrough
		out["smtp_response"] = *rep.failureDetail.SMTPResponse
	}
	if rep.failureDetail.ErrorClass != nil {
		// copy-ledger: report.error_class_passthrough
		out["error_class"] = *rep.failureDetail.ErrorClass
	}
	if rep.failureDetail.FailedAt != nil {
		out["failed_at"] = rep.failureDetail.FailedAt.UTC()
	}
	writeJSON(w, http.StatusOK, out)
}

// --- GET /v1/describe — module self-description: capabilities + tiers/pricing (Phase 9 §2, finalized) ---

// handleDescribe reports the module's honest self-description (Phase 9 §2.2): the tiers/pricing
// shape, flat SUBSCRIPTION pricing (M3-P7-OD-11 — module-3-build-plan.md §1: no per-send charge,
// no per-recipient price), and the capabilities the module actually implements — no sequences, no
// open/click tracking, content variation present as operator-tunable behavior (no per-toggle
// detail, Phase 8 §3). Reads the SAME live unit_price_credits quote (§1) multiplies by, via
// describeTiers — single source of truth, so describe and quote can never disagree (Phase 9 §2.2,
// E2). unit_price_credits/tiers[].unit_price_credits are retained at 0 for Rule-5 contract
// compatibility with module 2's additive shape, not meaningful pricing under this module.
func (s *Server) handleDescribe(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r.Context())
	defer cancel()
	unit := s.readUnitPriceCredits(ctx)
	// fix-plan item 8. Read LIVE from pg_enum on every describe — see describe_vocabularies.go's
	// header for why this one FAILS the request rather than falling back the way readUnitPriceCredits
	// above deliberately does (a tunable integer has an honest default; a vocabulary does not, and the
	// only available fallback is the stale hardcoded list this item exists to delete).
	vocabularies, verr := s.describeVocabularies(ctx)
	if verr != nil {
		s.fail(w, r, verr)
		return
	}
	// fix-plan item 14: DERIVED from the served route table (the strings a client's URLs must actually
	// match), never a literal here. This module flattens 22 routes from two declaration sites, 18 of
	// them registry data — so a literal here and the paths were two unrelated strings that agreed by
	// luck, and the likely break is a 19th extension route added under a different prefix.
	//
	// No nil/empty branch, deliberately: mustAPIVersion refuses at construction, so by the time any
	// request reaches this line the version is guaranteed derivable. M2's break B91 showed a branch
	// HERE is unreachable and therefore untestable — see mustAPIVersion's header.
	writeJSON(w, http.StatusOK, map[string]any{
		"module":      "send_byo",
		"api_version": s.APIVersion(),
		"capabilities": map[string]any{
			"single_send":           true,
			"sequences":             false,
			"reply_to_per_campaign": true,
			"open_tracking":         false,
			"click_tracking":        false,
			"content_variation":     true,
			// Phase 3 close-out C5, additive: the module sends a multipart/alternative message (text/plain first,
			// text/html last) for a job whose public.job_items.rendered_text is set, and the single text/html
			// message it always sent when it is NULL, empty or whitespace-only. The field itself is a COLUMN of the
			// platform-owned job_items table, not an API request field, so it is advertised as a capability here
			// rather than as a schema (describe publishes no request schemas for the core routes).
			"plain_text_alternative": true,
			// Phase 4 C1, additive: the module can hold ONE campaign and release it later — POST /v1/pause and
			// POST /v1/resume (campaign_pause.go) — and the campaign report says which with `paused`. Declared here
			// the way plain_text_alternative is, as a capability the platform's adapter can read, because describe
			// publishes no request schemas for the core routes. Always true: it is a property of this build, not of
			// a deployment's configuration.
			"pause_resume": true,
		},
		"pricing": map[string]any{
			"currency": "credits",
			"model":    pricingModelFlatSubscription,
			// fix-plan item 6: the declared value set, so a client can switch on `model` without
			// already knowing every module's answer. ADDITIVE-ONLY (Rule 5) and deliberately WIDER than
			// what any module reports today — it includes `free`, which neither sender emits, because
			// the point is that a client writes its branch once and a third module's launch is not a
			// client-side code change. The control asserts `model` is a MEMBER of `models`, so this
			// module cannot report a value it did not declare.
			"models":             pricingModels,
			"unit_price_credits": unit,
			"tiers":              describeTiers(unit),
			// D22 / fix-plan 5b: quote's `cost` is a REAL number that is legitimately 0 under this
			// module's flat-subscription pricing — indistinguishable, to a caller, from a broken
			// price lookup that happened to return 0. That silence is the defect: D4 returns a
			// detectable 400, this returns 200 with a number a cost-preview UI will render as
			// "0 credits" with no error. The canonical contract rules the FIELDS stay (§1.6: "a
			// module with no per-unit charge reports unit_price_credits: 0 and cost: 0 ... the right
			// call"), so the fix is not to change the number — it is to say so out loud here, in the
			// verb whose whole job is telling a client what it would otherwise hardcode.
			//
			// DERIVED FROM `unit`, NEVER HARDCODED. unit_price_credits is live-retunable from
			// send_byo.config on every request (Rule 1, readUnitPriceCredits) and send_byo_0048 only
			// changed its DEFAULT — the mechanism is still live. A literal `true` here would become
			// a lie the first time an operator retunes the key, and it would be a lie in the one
			// direction that costs money: a client told the cost half is inert while it is actually
			// charging. Deriving it means the declaration cannot outlive the condition it describes.
			// The retune half of TestUnitPrice_QuoteAndDescribeReflectLiveConfig is the control:
			// PUT unit_price_credits=3 must flip this to false, so a hardcoded `true` fails.
			"cost_fields_inert": unit == 0,
		},
		// Phase 7 §5.1: the extension routes beyond the core four, generated from the SAME slice
		// server.go's routes() mounts (describe_routes.go) — cannot drift, by construction. NEVER
		// includes an operator (/v1/admin/*) route (§5.1's security half: describe is served on
		// Domain A, and enumerating the admin surface to a platform token is a free map of it).
		"extension_routes": describeExtensionRoutesJSON(s.extensionRoutes()),
		"schemas":          describeSchemas(),
		// fix-plan item 8, the two additions that turn `schemas` from "what shape" into "what shape AND
		// what values". vocabularies{} publishes every enum a response on this surface can carry, read
		// live from the schema; error_codes[] publishes what this module can emit, derived from the SAME
		// codeStatus map writeError resolves a status from, so it can neither omit an emittable code nor
		// invent one. Both exist because a UI that cannot ask gets its answers from prose and breaks
		// silently when a value is added — the failure additive-only versioning was meant to prevent.
		"vocabularies": vocabularies,
		"error_codes":  describeErrorCodes(),
		// bulk_add's UI copy contract (Phase 3 §4.3): the module has no UI, so what it owns is the
		// CONTRACT the UI must honor. warning_code is versioned and additive-only (Rule 5);
		// warning_text is the required baseline the UI MUST display before the file picker and
		// MUST NOT suppress or defer — a UI may reword it, but must convey all four points the
		// text below states, especially the fourth ("the file is not a backup"), which is the one
		// most likely to be silently dropped and the one that changes user behavior if it is.
		"bulk_add": map[string]any{
			"warning_code": "credentials_in_plaintext_file",
			"warning_text": "The file you are about to upload contains plaintext SMTP passwords. " +
				"It exists on your own disk (and anywhere it has been emailed or synced to) until you " +
				"delete it. Delete it after upload. We store your credentials encrypted and never show " +
				"them again, so this file is not a backup you can rely on — keeping it is pure downside.",
		},
	})
}

// readUnitPriceCredits reads unit_price_credits fresh from send_byo.config (Phase 9.5 S10, §2.4;
// zeroed under M3-P7-OD-11's flat-subscription ruling) — live-retunable, no redeploy. platformapi
// does NOT import internal/dispatch's LoadConfig (structural package isolation, OD-7's precedent —
// mirrors internal/adminapi's own local config readers), so this is a minimal, scoped-to-one-key
// local reader. Falls back to the seeded default on any read/parse failure rather than failing the
// request — quote/describe are read-mostly, low-stakes endpoints and a transient config-read blip
// must not 500 every quote in flight.
func (s *Server) readUnitPriceCredits(ctx context.Context) int {
	var raw []byte
	if err := s.db.QueryRow(ctx, `SELECT value FROM send_byo.config WHERE key = 'unit_price_credits'`).Scan(&raw); err != nil {
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
		e := newErr(codeValidationFailed, "malformed_json: "+sanitizeDecodeErr(err))
		// NAME THE FIELD when the decoder knows it. A wrong-TYPE value (`{"recipient_count": "7"}`)
		// returned an EMPTY details[] until 2026-07-28, so a caller got validation_failed with nothing
		// to attach it to — the one class of rejection where the body IS well-formed JSON and exactly
		// one named field is at fault (found walking Stage 7 of the local test guide). details[] is
		// documented as optional/additive, so this is a diagnosability fix, not a contract change:
		// the code and status are unchanged.
		//
		// TWO-VOCABULARY SAFETY (Rule 5): only *json.UnmarshalTypeError is unwrapped, and only its
		// .Field is used. That name is the key that MATCHED one of OUR struct tags (encoding/json
		// matches case-insensitively), so it is bounded to this endpoint's own schema — never arbitrary
		// caller content, and never the offending VALUE. A DisallowUnknownFields rejection is
		// deliberately NOT given a detail: its field name is attacker-chosen text from the body.
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) && typeErr.Field != "" {
			e = e.withDetails(fieldDetail{Field: typeErr.Field, Issue: issueMalformed})
		}
		return e
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
