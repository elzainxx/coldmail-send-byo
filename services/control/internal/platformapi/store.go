package platformapi

import (
	"context"
	"time"

	"github.com/coldmail-send-byo/control/internal/userconfig"
)

// SQLSTATE constants (never surfaced; used to translate to clean codes).
const (
	sqlstateUniqueViolation     = "23505"
	sqlstateForeignKeyViolation = "23503"
	sqlstateCheckViolation      = "23514"
)

// The two statements `run` issues against the platform's public.job_items. They are named constants (each one literal, never
// built by concatenation) because the platform's drift gate (db/module-statements.txt, labels J1 and J2) holds their TEXT, and
// because platformapi's structural test reads them: the cancelled-item guard is the SAME single named exclusion in both.
//
// jobItemReadSQL (J1) reads the item for a run, scoped to the calling tenant: the campaign the item belongs to and whether the
// platform has marked it cancelled, in ONE statement. `status IS NOT DISTINCT FROM 'cancelled'` is true for that one value and
// never NULL, so a NULL status (the fixture tables in this repository declare the column with no default and no NOT NULL) reads
// as "not cancelled" instead of poisoning the Scan; every other value, the default 'pending' included, is "not cancelled" too.
//
// jobItemFreezeSQL (J2) freezes the item into send_byo.jobs (the genesis job_events row is written by a trigger) and carries the
// SAME exclusion as a predicate of its own SELECT: `ji.status IS DISTINCT FROM 'cancelled'`, the negation of J1's test and
// NULL-safe in the same way. The predicate is not a repeat for show. J1 and J2 are two statements, and a marker the platform
// commits between them must not slip a run through; J2 tests the status and creates the row in ONE statement under ONE
// snapshot, so there is no instant between the check and the insert for a marker to land in. A row lock on the item (the other
// way to close the gap) is not available: the module's role holds SELECT and INSERT on that table and never UPDATE, and
// SELECT ... FOR SHARE needs UPDATE.
//
// J2 also copies public.job_items.unsubscribe_url (Phase 5 S4: the per-recipient unsubscribe link, a nullable column the PLATFORM adds
// and writes) into send_byo.jobs.unsubscribe_url, beside rendered_text. A NULL stays NULL (the message is then exactly as it was, with
// neither List-Unsubscribe header); a value the module's own CHECK (jobs_unsubscribe_url_check, send_byo_0060) refuses is the same
// 23514 -> 400 validation_failed the other copied columns answer. J1 is unchanged: it needs no part of the link.
const (
	jobItemReadSQL = `SELECT campaign_id, status IS NOT DISTINCT FROM 'cancelled' FROM public.job_items WHERE id = $1 AND user_id = $2`

	jobItemFreezeSQL = `INSERT INTO send_byo.jobs
	   (job_item_id, campaign_id, user_id, recipient_email, rendered_subject, rendered_body, rendered_text, unsubscribe_url, reply_to, state, next_attempt_at)
	 SELECT ji.id, ji.campaign_id, ji.user_id, ji.recipient_email,
	        ji.rendered_subject, ji.rendered_body, ji.rendered_text, ji.unsubscribe_url, ji.reply_to, 'queued', NOW()
	 FROM public.job_items ji
	 WHERE ji.id = $1 AND ji.user_id = $2 AND ji.status IS DISTINCT FROM 'cancelled'
	 RETURNING id`
)

// createJob creates one send_byo.jobs row in state 'queued' from the platform-owned job_item
// (read with SELECT here; the module's one write to public.job_items is test-send's INSERT, send_byo_0037), with the actor='system' GUC so the genesis event
// is written (§6.2). Returns (jobID, existing, err): existing=true when the job_item already has
// a sender job (the jobs_job_item_id_unique constraint) — the run is idempotent, never a raw 23505.
//
// THE CANCELLED-ITEM GUARD (ADR-0022). The platform gives a never-submitted item of a job that expired, was cancelled or failed
// the terminal marker public.job_items.status = 'cancelled'. No module may act on such a row, so a run of one is REFUSED with
// `job_item_cancelled` (409) and no job row is written. Both statements that touch the item carry the exclusion (jobItemReadSQL
// and jobItemFreezeSQL above); the refusal is answered only for an item of the CALLER'S tenant (another tenant's cancelled item
// is the same 404 as an id that does not exist); and every other status runs exactly as before. It is answered BEFORE the four
// causes of a 409 are read, so it does not depend on the user's suspension, on a pause of the item's campaign or on any cap: a
// dead item is a permanent fact and must not be hidden behind a transient one the platform would wait out and retry.
//
// WHICH WINS when the item already has a module job: THE REPLAY. The existing-job lookup is the first statement of this
// function and returns before the item is read at all, so a job accepted before the marker keeps answering 200 with its current
// state however the item is marked later — it already ran, the marker is the platform's record and not a lock, and a job the
// module accepted is stopped (if the platform wants it stopped) by POST /v1/cancel, never by the marker. The guard also yields to
// a replay in the one interleave where it could otherwise contradict it: see replayOrRefuseCancelled.
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
// commit — that half of M3-P1-OD-4 is registered as FW-M3-20 in Docs/future-work.md).
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

	if _, err := tx.Exec(ctx, "SELECT set_config('send_byo.actor', 'system', true)"); err != nil {
		return "", false, newErr(codeInternalError, "set_actor_failed")
	}

	// Serialize concurrent campaign-start checks for THIS user (transaction-scoped: auto-released
	// on commit/rollback, no unlock call needed). hashtext() keeps the key namespace distinct from
	// both the Control singleton lock and the migration runner's lock (Docs/architecture.md#advisory-locks).
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext('send_byo.campaign_start:' || $1::text))`, userID); err != nil {
		return "", false, newErr(codeInternalError, "advisory_lock_failed")
	}

	// THE FOUR CAUSES OF A 409 (the Phase 3 close-out, C2; run_causes.go). `run` refuses for exactly four
	// reasons — the user is suspended (§6.4: a suspended user's enqueue is refused outright, accepting work
	// into a queue that cannot drain is a growing debt against the queue cap), ANOTHER campaign is still
	// active, THIS campaign already holds max_campaign_size jobs, the user's live queue already holds
	// max_queued_jobs — and used to answer one opaque 409 for all of them. Every cause that applies is now
	// collected (NOT first-match: two can hold at once) from the SAME facts `quote` reads, and the 409 carries
	// one details[] entry per cause. The status, the code and the message are unchanged.
	//
	// The job item is read first because the campaign facts depend on its campaign_id; a cross-tenant job_item
	// still resolves to job_item_not_found via the same tenant scoping, never leaking whether it exists. A
	// SUSPENDED user is still refused before anything about job items is revealed: with no item found the
	// answer is the one cause known without it.
	var (
		campaignID string
		cancelled  bool
	)
	itemFound := true
	if err := tx.QueryRow(ctx, jobItemReadSQL, jobItemID, userID).Scan(&campaignID, &cancelled); err != nil {
		if !isNoRows(err) {
			return "", false, newErr(codeInternalError, "read_job_item_failed")
		}
		itemFound, campaignID = false, ""
	}
	// The platform has marked this item cancelled: refuse it (or, where a committed job already exists, replay it) BEFORE the
	// causes of a 409 are read, so the answer is about the item alone and does not move with the user's suspension, a pause of
	// its campaign or a cap. Nothing has been written yet. Only a FOUND item (this tenant's own) can be cancelled here: another
	// tenant's item reads as not found above and takes the not_found path below, indistinguishable from a missing id.
	if itemFound && cancelled {
		return s.replayOrRefuseCancelled(ctx, jobItemID, userID, "job_item_cancelled_at_read")
	}
	facts, ferr := readRunCauseFacts(ctx, tx, userID, campaignID)
	if ferr != nil {
		return "", false, newErr(codeInternalError, "run_cause_facts_failed")
	}
	if !itemFound {
		if facts.Suspended {
			return "", false, runConflict([]fieldDetail{runCauseDetail(runCauseSuspended, nil, nil)})
		}
		return "", false, newErr(codeNotFound, "job_item_not_found").
			withDetails(fieldDetail{Field: "job_item_id", Issue: issueNotFound})
	}

	// §5.2's two resource caps, each resolved override→global (§10.2 — Phase 6 owns consolidating
	// the resolution path; this reads the two sites it needs directly). Evaluated per insert (§5.1:
	// one job per /v1/run call, so a caller cannot under-declare its way past either cap).
	maxCampaignSize, maxQueuedJobs, campaignExpirySec, cerr := s.resolveCampaignLimits(ctx, tx, userID)
	if cerr != nil {
		return "", false, cerr
	}
	if causes := facts.causes(1, maxCampaignSize, maxQueuedJobs); len(causes) > 0 {
		return "", false, runConflict(causes)
	}

	// §5.3: upsert the campaign row on the FIRST job of a campaign — expires_at is resolved and
	// FROZEN here (a later config/override change never retroactively moves an already-started
	// campaign's expiry). ON CONFLICT DO NOTHING: a second+ job of the same campaign leaves the
	// existing row (and its already-frozen expires_at) untouched.
	if _, err := tx.Exec(ctx,
		`INSERT INTO send_byo.campaigns (id, user_id, expires_at)
		 VALUES ($1, $2, NOW() + make_interval(secs => $3))
		 ON CONFLICT (id) DO NOTHING`,
		campaignID, userID, campaignExpirySec); err != nil {
		return "", false, newErr(codeInternalError, "campaign_upsert_failed")
	}

	// Freeze the denormalized copy from public.job_items into send_byo.jobs (Phase 1 shape; rendered_text, the
	// optional plain-text alternative, since Phase 3 close-out C5), but ONLY
	// if the job_item belongs to the calling tenant (`ji.user_id = $2`) AND the platform has not marked it cancelled
	// (jobItemFreezeSQL's last predicate: the status test and the insert are this ONE statement, so the refusal is atomic
	// with the creation). A cross-tenant job_item matches 0 rows → job_item_not_found (404-style), identical to a
	// non-existent job_item — the module never confirms another tenant's job_item exists.
	err = tx.QueryRow(ctx, jobItemFreezeSQL, jobItemID, userID).Scan(&jobID)
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
			// The freeze matched nothing. The read above found this tenant's item a moment ago (the not-found case returned
			// before here), so the likeliest cause is the marker landing between the two statements; look again, in this
			// transaction (a fresh statement, so a fresh snapshot), to say WHICH refusal is true instead of guessing. This
			// re-read only CLASSIFIES a refusal that jobItemFreezeSQL has already made: it admits nothing.
			var (
				campaign     string
				cancelledNow bool
			)
			rerr := tx.QueryRow(ctx, jobItemReadSQL, jobItemID, userID).Scan(&campaign, &cancelledNow)
			switch {
			case rerr == nil && cancelledNow:
				return s.replayOrRefuseCancelled(ctx, jobItemID, userID, "job_item_cancelled_before_insert")
			case rerr == nil:
				// The item exists, is the tenant's and is not cancelled, and yet the freeze returned no row: not a state
				// the table's vocabulary can produce (a marker is never moved back).
				return "", false, newErr(codeInternalError, "create_job_failed")
			case isNoRows(rerr):
				// No such job_item FOR THIS TENANT (unknown id, or owned by another tenant). Same
				// clean 404-style outcome either way — no cross-tenant existence disclosure.
				return "", false, newErr(codeNotFound, "job_item_not_found").
					withDetails(fieldDetail{Field: "job_item_id", Issue: issueNotFound})
			default:
				return "", false, newErr(codeInternalError, "read_job_item_failed")
			}
		}
		return "", false, newErr(codeInternalError, "create_job_failed")
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, newErr(codeInternalError, "commit_failed")
	}
	return jobID, false, nil
}

// replayOrRefuseCancelled is the answer for an item the platform has marked cancelled, and it is the one place the guard
// yields: if the item ALREADY has a module job (found on the pool, i.e. committed), that job is the answer and the run is the
// idempotent replay it has always been. That can only matter in one interleave: two first runs of the same item serialize on the
// per-user advisory lock above, the first creates the job and commits, the platform marks the item, and the second, which passed
// the existing-job lookup before the first committed, would otherwise report a cancelled item that does have a job. Otherwise
// nothing exists for this item and the run is refused, with the internal reason saying which of the two statements saw the
// marker (logged against the request id, never serialized). Either way nothing is written by this call.
func (s *Server) replayOrRefuseCancelled(ctx context.Context, jobItemID, userID, reason string) (string, bool, *apiError) {
	if existingID, ok := s.jobIDByItem(ctx, jobItemID, userID); ok {
		return existingID, true, nil
	}
	return "", false, newErr(codeJobItemCancelled, reason)
}

// resolveCampaignLimits resolves the three §5.2/§5.3 dials for userID through userconfig.Resolve —
// Module 3 Phase 6 §1's ONE resolution path (`ResolveUserConfig`'s contract). This function used to
// carry its own inline override→global logic (a deliberate Phase 5 §10.2 placeholder: "Phase 6
// consolidates. Noted here so Phase 6 knows this site exists.") — deleted, not merely supplemented,
// per §1.4/§9 check 1's mechanical requirement (`grep` for a second resolver must return nothing).
// The other three dials (SendDelayMinSec/SendDelayMaxSec/ConcurrencyPerUser) are irrelevant to this
// call site and simply ignored — this function still returns only the three createJob needs.
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

	uc, err := userconfig.Resolve(ctx, tx, userID, userconfig.Globals{
		MaxCampaignSize:   globalCampaignSize,
		MaxQueuedJobs:     globalQueuedJobs,
		CampaignExpirySec: globalExpirySec,
	})
	if err != nil {
		return 0, 0, 0, newErr(codeInternalError, "user_config_overrides_lookup_error")
	}
	return uc.MaxCampaignSize, uc.MaxQueuedJobs, uc.CampaignExpirySec, nil
}

// jobIDByItem returns the existing sender job id for a job_item OWNED BY userID, if any.
func (s *Server) jobIDByItem(ctx context.Context, jobItemID, userID string) (string, bool) {
	var id string
	err := s.db.QueryRow(ctx,
		`SELECT id FROM send_byo.jobs WHERE job_item_id = $1 AND user_id = $2`, jobItemID, userID).Scan(&id)
	if err != nil {
		return "", false
	}
	return id, true
}

// jobStateByItem returns the current state for a job_item's sender job, scoped to userID.
func (s *Server) jobStateByItem(ctx context.Context, jobItemID, userID string) (string, bool) {
	var state string
	err := s.db.QueryRow(ctx,
		`SELECT state::text FROM send_byo.jobs WHERE job_item_id = $1 AND user_id = $2`, jobItemID, userID).Scan(&state)
	if err != nil {
		return "", false
	}
	return state, true
}

// reportRow is the clean per-job report shape (§6.3), widened by Phase 7 §1.1/§1.5 (failure
// detail) and §2 (per-account attribution). The failure-detail columns provider_text.go's
// accessor governs (see providerFailureSelectCols) are read here — this function is P1's half of
// the "single named accessor" pattern, alongside campaign_report.go's campaignItems for P2 — and
// exposed via failureDetail. The internal reason column (§1.4) is never read here, by
// construction: this SELECT does not name it. accountID/accountUsername are read via a LEFT JOIN
// (§2.2) so a job with no assigned account (never dispatched, or requeued and cleared) reports
// nulls rather than dropping the row or erroring.
type reportRow struct {
	jobID             string
	state             string
	providerMessageID *string
	completedAt       *time.Time
	attemptCount      int
	accountID         *string
	accountUsername   *string
	failureDetail     providerFailureDetail
}

// readReport fetches the clean state for a job by job_id OR job_item_id, SCOPED to the calling
// tenant (userID). Only the external-safe columns are selected (§6.3, §1.4/§1.5) — the internal
// reason column, the worker's raw error slug, and any credential are never read here. A job that
// exists but belongs to another tenant matches 0 rows → (false) → the handler returns the SAME
// 404-style not-found as a non-existent job, so a caller cannot distinguish "not mine" from "does
// not exist" (no existence disclosure — OD-25).
//
// §2.2's LEFT JOIN: a job's account always belongs to the same user in module 3 (dispatch only
// ever assigns a same-user account), but tenancy here is established by `j.user_id = $2` alone —
// the join is never what makes this tenant-safe, it only supplies the username when present.
func (s *Server) readReport(ctx context.Context, jobID, jobItemID, userID string) (reportRow, bool, error) {
	var (
		rr           reportRow
		smtpResponse *string
		errorClass   *string
		err          error
	)
	const cols = `j.id, j.state::text, j.provider_message_id, j.completed_at, j.attempt_count,
		             j.assigned_account_id, a.smtp_username, `
	if jobID != "" {
		err = s.db.QueryRow(ctx,
			`SELECT `+cols+providerFailureSelectCols+`
			 FROM send_byo.jobs j LEFT JOIN send_byo.accounts a ON a.id = j.assigned_account_id
			 WHERE j.id = $1 AND j.user_id = $2`, jobID, userID).
			Scan(&rr.jobID, &rr.state, &rr.providerMessageID, &rr.completedAt, &rr.attemptCount,
				&rr.accountID, &rr.accountUsername, &smtpResponse, &errorClass)
	} else {
		err = s.db.QueryRow(ctx,
			`SELECT `+cols+providerFailureSelectCols+`
			 FROM send_byo.jobs j LEFT JOIN send_byo.accounts a ON a.id = j.assigned_account_id
			 WHERE j.job_item_id = $1 AND j.user_id = $2`, jobItemID, userID).
			Scan(&rr.jobID, &rr.state, &rr.providerMessageID, &rr.completedAt, &rr.attemptCount,
				&rr.accountID, &rr.accountUsername, &smtpResponse, &errorClass)
	}
	if err != nil {
		if isNoRows(err) {
			return reportRow{}, false, nil
		}
		return reportRow{}, false, err
	}
	rr.failureDetail = readProviderFailureDetail(rr.state, smtpResponse, errorClass, rr.completedAt)
	return rr, true, nil
}
