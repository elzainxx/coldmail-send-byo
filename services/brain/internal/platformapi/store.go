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

// createJob creates one sender.jobs row in state 'queued' from the platform-owned job_item
// (SELECT-only on public.job_items, Phase 1), with the actor='system' GUC so the genesis event
// is written (§6.2). Returns (jobID, existing, err): existing=true when the job_item already has
// a sender job (the jobs_job_item_id_unique constraint) — the run is idempotent, never a raw 23505.
func (s *Server) createJob(ctx context.Context, jobItemID string) (jobID string, existing bool, e *apiError) {
	// If a job for this job_item already exists, return it (idempotent create, §6.2).
	if existingID, ok := s.jobIDByItem(ctx, jobItemID); ok {
		return existingID, true, nil
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", false, newErr(codeInternalError, "begin_tx_failed")
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('sender.actor', 'system', true)"); err != nil {
		return "", false, newErr(codeInternalError, "set_actor_failed")
	}

	// Freeze the denormalized copy from public.job_items into sender.jobs (Phase 1 shape). The
	// module reads only the columns the platform guarantees; it never writes public.*.
	err = tx.QueryRow(ctx,
		`INSERT INTO sender.jobs
		   (job_item_id, campaign_id, user_id, recipient_email, rendered_subject, rendered_body, reply_to, state, next_attempt_at)
		 SELECT ji.id, ji.campaign_id, ji.user_id, ji.recipient_email,
		        ji.rendered_subject, ji.rendered_body, ji.reply_to, 'queued', NOW()
		 FROM public.job_items ji
		 WHERE ji.id = $1
		 RETURNING id`, jobItemID).Scan(&jobID)
	if err != nil {
		switch pgCode(err) {
		case sqlstateUniqueViolation:
			// Raced with another run for the same job_item_id: fetch and return the existing job.
			if existingID, ok := s.jobIDByItemTx(ctx, jobItemID); ok {
				_ = tx.Rollback(ctx)
				return existingID, true, nil
			}
			return "", false, newErr(codeConflict, "job_item_already_accepted")
		case sqlstateForeignKeyViolation, sqlstateCheckViolation:
			return "", false, newErr(codeValidationFailed, "job_item_invalid")
		}
		if isNoRows(err) {
			// No such job_item (the SELECT matched nothing → INSERT ... SELECT inserted 0 rows).
			return "", false, newErr(codeValidationFailed, "job_item_not_found")
		}
		return "", false, newErr(codeInternalError, "create_job_failed")
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, newErr(codeInternalError, "commit_failed")
	}
	return jobID, false, nil
}

// jobIDByItem returns the existing sender job id for a job_item, if any.
func (s *Server) jobIDByItem(ctx context.Context, jobItemID string) (string, bool) {
	var id string
	err := s.db.QueryRow(ctx, `SELECT id FROM sender.jobs WHERE job_item_id = $1`, jobItemID).Scan(&id)
	if err != nil {
		return "", false
	}
	return id, true
}

// jobIDByItemTx is the same lookup on the pool (used on the unique-violation race path).
func (s *Server) jobIDByItemTx(ctx context.Context, jobItemID string) (string, bool) {
	return s.jobIDByItem(ctx, jobItemID)
}

// jobStateByItem returns the current state for a job_item's sender job.
func (s *Server) jobStateByItem(ctx context.Context, jobItemID string) (string, bool) {
	var state string
	err := s.db.QueryRow(ctx, `SELECT state::text FROM sender.jobs WHERE job_item_id = $1`, jobItemID).Scan(&state)
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

// readReport fetches the clean state for a job by job_id OR job_item_id. Only the external-safe
// columns are selected (§6.3); the SMTP response / error reason / credential are never read here.
func (s *Server) readReport(ctx context.Context, jobID, jobItemID string) (reportRow, bool, error) {
	var (
		rr  reportRow
		err error
	)
	if jobID != "" {
		err = s.db.QueryRow(ctx,
			`SELECT id, state::text, provider_message_id, completed_at, attempt_count
			 FROM sender.jobs WHERE id = $1`, jobID).
			Scan(&rr.jobID, &rr.state, &rr.providerMessageID, &rr.completedAt, &rr.attemptCount)
	} else {
		err = s.db.QueryRow(ctx,
			`SELECT id, state::text, provider_message_id, completed_at, attempt_count
			 FROM sender.jobs WHERE job_item_id = $1`, jobItemID).
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
