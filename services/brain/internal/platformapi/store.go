package platformapi

import (
	"context"
	"time"
)

// SQLSTATE constants (never surfaced; used to translate to clean codes).
const (
	sqlstateUniqueViolation     = "23505"
	sqlstateForeignKeyViolation = "23503"
	sqlstateCheckViolation      = "23514"
)

// createJob creates one byosmtp.jobs row in state 'queued' from the platform-owned job_item
// (SELECT-only on public.job_items, Phase 1), with the actor='system' GUC so the genesis event
// is written (§6.2). Returns (jobID, existing, err): existing=true when the job_item already has
// a sender job (the jobs_job_item_id_unique constraint) — the run is idempotent, never a raw 23505.
//
// TENANT-SCOPED (OD-25 option b): the caller's tenant (userID) is enforced. The job_item must
// BELONG to userID (`ji.user_id = userID`); a job_item owned by another tenant is treated as
// NOT FOUND — no existence disclosure across tenants. The idempotent-existing lookup is scoped
// the same way, so tenant B can never observe (nor collide with) tenant A's job for A's job_item.
//
// ONE-ACTIVE-CAMPAIGN-PER-USER (§8, P4-7, Phase 4 Chunk 5): the whole check-then-insert runs under
// a per-user PostgreSQL transaction-scoped advisory lock (released on commit/rollback), so two
// concurrent `run` calls for the SAME user cannot both pass the "is a different campaign already
// active" check. Starting a SECOND campaign for a user who already has one active is REJECTED
// outright — no queueing, NO job row created (409 conflict, internal reason
// 'user_campaign_active') — while a REPEAT `run` for the SAME campaign_id is unaffected (handled
// above by the idempotent existing-job lookup) and always passes this gate.
//
// Module 3 Phase 5 additions, all inside the SAME advisory-locked transaction (§5.2/§5.3/§6.4):
// the per-user suspend gate (a suspended user's enqueue is refused outright — accepting work into
// a queue that cannot drain is not losslessness, it is a growing debt against the queue cap); the
// campaign-size cap (A) and the user's total-live-queue cap (B), each resolved override→global;
// and the campaign row upsert that freezes campaign_expiry_sec at the FIRST job of a campaign
// (M3-P5-OD-6: the upsert is built now, but jobs.campaign_id gains no FK to campaigns in this
// commit — that half of M3-P1-OD-4 stays open).
func (s *Server) createJob(ctx context.Context, jobItemID, userID string) (jobID string, existing bool, e *apiError) {
	// If a job for this job_item already exists FOR THIS TENANT, return it (idempotent, §6.2).
	// Same-campaign reruns therefore never reach the advisory-lock gate below.
	if existingID, ok := s.jobIDByItem(ctx, jobItemID, userID); ok {
		return existingID, true, nil
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", false, newErr(codeInternalError, "begin_tx_failed")
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('byosmtp.actor', 'system', true)"); err != nil {
		return "", false, newErr(codeInternalError, "set_actor_failed")
	}

	// Serialize concurrent campaign-start checks for THIS user (transaction-scoped: auto-released
	// on commit/rollback, no unlock call needed). hashtext() keeps the key namespace distinct from
	// both the Brain singleton lock and the migration runner's lock (Docs/advisory-locks.md).
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext('byosmtp.campaign_start:' || $1::text))`, userID); err != nil {
		return "", false, newErr(codeInternalError, "advisory_lock_failed")
	}

	// §6.4: a suspended user's enqueue is refused outright, under the SAME lock as the caps below
	// — checked first since it needs nothing else read yet (cheapest gate, no wasted work).
	var suspended bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM byosmtp.user_suspensions WHERE user_id=$1 AND suspended=TRUE)`, userID).
		Scan(&suspended); err != nil {
		return "", false, newErr(codeInternalError, "suspend_check_failed")
	}
	if suspended {
		return "", false, newErr(codeConflict, "user_suspended")
	}

	// A DIFFERENT campaign already active for this user (any non-terminal job) → reject outright,
	// no job row written. This must run for THIS job_item's campaign_id, so read it first — a
	// cross-tenant job_item still resolves to job_item_not_found below via the same INSERT...
	// SELECT tenant scoping, never leaking whether it exists.
	var campaignID string
	if err := tx.QueryRow(ctx,
		`SELECT campaign_id FROM public.job_items WHERE id = $1 AND user_id = $2`, jobItemID, userID).
		Scan(&campaignID); err != nil {
		if isNoRows(err) {
			return "", false, newErr(codeNotFound, "job_item_not_found").
				withDetails(fieldDetail{Field: "job_item_id", Issue: issueNotFound})
		}
		return "", false, newErr(codeInternalError, "read_job_item_failed")
	}
	var otherCampaignActive bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM byosmtp.jobs
		                WHERE user_id = $1 AND campaign_id <> $2
		                  AND state IN ('queued','assigned','sending'))`,
		userID, campaignID).Scan(&otherCampaignActive); err != nil {
		return "", false, newErr(codeInternalError, "campaign_check_failed")
	}
	if otherCampaignActive {
		return "", false, newErr(codeConflict, "user_campaign_active")
	}

	// §5.2's two resource caps, each resolved override→global (§10.2 — Phase 6 owns consolidating
	// the resolution path; this reads the two sites it needs directly). Evaluated per insert (§5.1:
	// one job per /v1/run call, so a caller cannot under-declare its way past either cap).
	maxCampaignSize, maxQueuedJobs, campaignExpirySec, cerr := s.resolveCampaignLimits(ctx, tx, userID)
	if cerr != nil {
		return "", false, cerr
	}
	var campaignSize int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM byosmtp.jobs WHERE campaign_id = $1`, campaignID).Scan(&campaignSize); err != nil {
		return "", false, newErr(codeInternalError, "campaign_size_check_failed")
	}
	if campaignSize >= maxCampaignSize {
		return "", false, newErr(codeConflict, "max_campaign_size_reached")
	}
	var queuedJobs int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM byosmtp.jobs WHERE user_id = $1 AND state IN ('queued','assigned','sending')`, userID).
		Scan(&queuedJobs); err != nil {
		return "", false, newErr(codeInternalError, "queued_jobs_check_failed")
	}
	if queuedJobs >= maxQueuedJobs {
		return "", false, newErr(codeConflict, "max_queued_jobs_reached")
	}

	// §5.3: upsert the campaign row on the FIRST job of a campaign — expires_at is resolved and
	// FROZEN here (a later config/override change never retroactively moves an already-started
	// campaign's expiry). ON CONFLICT DO NOTHING: a second+ job of the same campaign leaves the
	// existing row (and its already-frozen expires_at) untouched.
	if _, err := tx.Exec(ctx,
		`INSERT INTO byosmtp.campaigns (id, user_id, expires_at)
		 VALUES ($1, $2, NOW() + make_interval(secs => $3))
		 ON CONFLICT (id) DO NOTHING`,
		campaignID, userID, campaignExpirySec); err != nil {
		return "", false, newErr(codeInternalError, "campaign_upsert_failed")
	}

	// Freeze the denormalized copy from public.job_items into byosmtp.jobs (Phase 1 shape), but ONLY
	// if the job_item belongs to the calling tenant (`ji.user_id = $2`). A cross-tenant job_item
	// matches 0 rows → job_item_not_found (404-style), identical to a non-existent job_item — the
	// module never confirms another tenant's job_item exists.
	err = tx.QueryRow(ctx,
		`INSERT INTO byosmtp.jobs
		   (job_item_id, campaign_id, user_id, recipient_email, rendered_subject, rendered_body, reply_to, state, next_attempt_at)
		 SELECT ji.id, ji.campaign_id, ji.user_id, ji.recipient_email,
		        ji.rendered_subject, ji.rendered_body, ji.reply_to, 'queued', NOW()
		 FROM public.job_items ji
		 WHERE ji.id = $1 AND ji.user_id = $2
		 RETURNING id`, jobItemID, userID).Scan(&jobID)
	if err != nil {
		switch pgCode(err) {
		case sqlstateUniqueViolation:
			// Raced with another run for the same job_item_id (same tenant): fetch + return it.
			if existingID, ok := s.jobIDByItem(ctx, jobItemID, userID); ok {
				_ = tx.Rollback(ctx)
				return existingID, true, nil
			}
			return "", false, newErr(codeConflict, "job_item_already_accepted")
		case sqlstateForeignKeyViolation, sqlstateCheckViolation:
			return "", false, newErr(codeValidationFailed, "job_item_invalid")
		}
		if isNoRows(err) {
			// No such job_item FOR THIS TENANT (unknown id, or owned by another tenant). Same
			// clean 404-style outcome either way — no cross-tenant existence disclosure.
			return "", false, newErr(codeNotFound, "job_item_not_found").
				withDetails(fieldDetail{Field: "job_item_id", Issue: issueNotFound})
		}
		return "", false, newErr(codeInternalError, "create_job_failed")
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, newErr(codeInternalError, "commit_failed")
	}
	return jobID, false, nil
}

// resolveCampaignLimits resolves the three §5.2/§5.3 dials for userID: per-user override
// (byosmtp.user_config_overrides, NULL = inherit) if set, else the global byosmtp.config default.
// Reads the globals fresh via s.readConfigInt (config-not-code; mirrors the existing
// readAccountCountCap/resolveDailyLimit pattern on this surface) and the override row inside the
// caller's transaction, so the override read is consistent with the rest of this call's work.
// §10.2: this is ONE of the two sites Phase 5 needs this resolution at (the other is nowhere yet,
// since Phase 5 has no second consumer) — Phase 6 owns consolidating into a single resolution path
// once more call sites exist; this function is deliberately NOT exported or generalized past what
// this call site needs today.
func (s *Server) resolveCampaignLimits(ctx context.Context, tx pgxDB, userID string) (maxCampaignSize, maxQueuedJobs, campaignExpirySec int, e *apiError) {
	globalCampaignSize, ok1, err := s.readConfigInt(ctx, "max_campaign_size")
	if err != nil || !ok1 {
		return 0, 0, 0, newErr(codeInternalError, "max_campaign_size_lookup_error")
	}
	globalQueuedJobs, ok2, err := s.readConfigInt(ctx, "max_queued_jobs")
	if err != nil || !ok2 {
		return 0, 0, 0, newErr(codeInternalError, "max_queued_jobs_lookup_error")
	}
	globalExpirySec, ok3, err := s.readConfigInt(ctx, "campaign_expiry_sec")
	if err != nil || !ok3 {
		return 0, 0, 0, newErr(codeInternalError, "campaign_expiry_sec_lookup_error")
	}

	var oCampaignSize, oQueuedJobs, oExpirySec *int
	if err := tx.QueryRow(ctx,
		`SELECT max_campaign_size, max_queued_jobs, campaign_expiry_sec FROM byosmtp.user_config_overrides WHERE user_id=$1`,
		userID).Scan(&oCampaignSize, &oQueuedJobs, &oExpirySec); err != nil && !isNoRows(err) {
		return 0, 0, 0, newErr(codeInternalError, "user_config_overrides_lookup_error")
	}

	maxCampaignSize, maxQueuedJobs, campaignExpirySec = globalCampaignSize, globalQueuedJobs, globalExpirySec
	if oCampaignSize != nil {
		maxCampaignSize = *oCampaignSize
	}
	if oQueuedJobs != nil {
		maxQueuedJobs = *oQueuedJobs
	}
	if oExpirySec != nil {
		campaignExpirySec = *oExpirySec
	}
	return maxCampaignSize, maxQueuedJobs, campaignExpirySec, nil
}

// jobIDByItem returns the existing sender job id for a job_item OWNED BY userID, if any.
func (s *Server) jobIDByItem(ctx context.Context, jobItemID, userID string) (string, bool) {
	var id string
	err := s.db.QueryRow(ctx,
		`SELECT id FROM byosmtp.jobs WHERE job_item_id = $1 AND user_id = $2`, jobItemID, userID).Scan(&id)
	if err != nil {
		return "", false
	}
	return id, true
}

// jobStateByItem returns the current state for a job_item's sender job, scoped to userID.
func (s *Server) jobStateByItem(ctx context.Context, jobItemID, userID string) (string, bool) {
	var state string
	err := s.db.QueryRow(ctx,
		`SELECT state::text FROM byosmtp.jobs WHERE job_item_id = $1 AND user_id = $2`, jobItemID, userID).Scan(&state)
	if err != nil {
		return "", false
	}
	return state, true
}

// reportRow is the clean per-job report shape (§6.3).
type reportRow struct {
	jobID             string
	state             string
	providerMessageID *string
	completedAt       *time.Time
	attemptCount      int
}

// readReport fetches the clean state for a job by job_id OR job_item_id, SCOPED to the calling
// tenant (userID). Only the external-safe columns are selected (§6.3); the SMTP response / error
// reason / credential are never read here. A job that exists but belongs to another tenant matches
// 0 rows → (false) → the handler returns the SAME 404-style not-found as a non-existent job, so a
// caller cannot distinguish "not mine" from "does not exist" (no existence disclosure — OD-25).
func (s *Server) readReport(ctx context.Context, jobID, jobItemID, userID string) (reportRow, bool, error) {
	var (
		rr  reportRow
		err error
	)
	if jobID != "" {
		err = s.db.QueryRow(ctx,
			`SELECT id, state::text, provider_message_id, completed_at, attempt_count
			 FROM byosmtp.jobs WHERE id = $1 AND user_id = $2`, jobID, userID).
			Scan(&rr.jobID, &rr.state, &rr.providerMessageID, &rr.completedAt, &rr.attemptCount)
	} else {
		err = s.db.QueryRow(ctx,
			`SELECT id, state::text, provider_message_id, completed_at, attempt_count
			 FROM byosmtp.jobs WHERE job_item_id = $1 AND user_id = $2`, jobItemID, userID).
			Scan(&rr.jobID, &rr.state, &rr.providerMessageID, &rr.completedAt, &rr.attemptCount)
	}
	if err != nil {
		if isNoRows(err) {
			return reportRow{}, false, nil
		}
		return reportRow{}, false, err
	}
	return rr, true, nil
}
