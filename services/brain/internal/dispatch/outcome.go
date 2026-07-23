package dispatch

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sender-module/brain/internal/boxproto"
)

// OutcomeHandler applies a box `report` to the DB (§5.3–§5.5). It is installed as the stream
// server's ReportHandler. It NEVER invents an outcome (Rule 5) — it only acts on what the box
// reported — and it NEVER lets the credential reach send_logs/job_events/jobs (§5.5).
type OutcomeHandler struct {
	db  *pgxpool.Pool
	cfg Config
}

func NewOutcomeHandler(db *pgxpool.Pool, cfg Config) *OutcomeHandler {
	return &OutcomeHandler{db: db, cfg: cfg}
}

// Handle processes one report for a job produced by boxID. It is idempotent: a duplicate
// accepted report for an already-sent job is a clean no-op (§2.5, §5.3). boxID attributes
// provenance in job_events.
func (h *OutcomeHandler) Handle(boxID string, rep boxproto.Report) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Idempotency gate (§5.3): if the job already has a provider_message_id, it is already sent —
	// never send/persist again. This makes a replayed/duplicate accepted a no-op.
	already, curState, err := h.jobStatus(ctx, rep.JobID)
	if err != nil {
		log.Printf("outcome: cannot read job %s: %v", rep.JobID, err)
		return
	}
	if already {
		return // already sent; ignore (clean no-op)
	}
	// Only a job that is in-flight (sending) can transition on a report. A report for a job in any
	// other state (e.g. already requeued to queued, or unknown) is acknowledged and ignored (§2.5).
	if curState != "sending" {
		return
	}

	switch rep.Outcome {
	case boxproto.OutcomeAccepted:
		h.applyAccepted(ctx, boxID, rep)
	case boxproto.OutcomeBounced:
		h.applyBounced(ctx, boxID, rep)
	default: // error (or anything non-accepted/bounced) → requeue (§5.4)
		h.applyError(ctx, boxID, rep)
	}
}

// applyAccepted: one tx — set provider_message_id + state='sent' + completed_at + clear lease,
// write the send_logs row, and (trigger) the event. provider_message_id is written at the SAME
// commit that marks sent, so there is no window where a job is 'sent' with a null id (§5.3). A
// report claiming accepted with NO id is impossible per §4.3 (the box would send
// error/accepted_without_id); guard anyway.
//
// daily_sent_count is NOT incremented here (Phase 4 change, §7.1/§7.3): the atomic account-consume
// (eligibility.go) increments it BEFORE the send is attempted, in the same statement that admits
// the row — that is the only ordering that prevents two concurrent workers from both passing an
// eligibility check for an account's last unit of headroom. Incrementing again on accept would
// double-count. The accepted tradeoff (§7.3): a consumed-but-never-delivered slot (send fails/
// crashes after consume) still costs headroom; refunding it is deferred to Phase 6 (P4-15).
func (h *OutcomeHandler) applyAccepted(ctx context.Context, boxID string, rep boxproto.Report) {
	if rep.ProviderMessageID == "" {
		// Defensive: never mark sent without an idempotency key. Treat as an error requeue.
		log.Printf("outcome: job %s accepted with empty provider_message_id — requeued (§4.3 anomaly)", rep.JobID)
		h.applyError(ctx, boxID, boxproto.Report{JobID: rep.JobID, Outcome: boxproto.OutcomeError, ErrorReason: boxproto.ReasonAcceptedWithoutID, SMTPResponse: rep.SMTPResponse})
		return
	}
	err := h.inActorTx(ctx, "box", func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE sender.jobs
			   SET state='sent',
			       provider_message_id=$2,
			       completed_at=NOW(),
			       lease_expires_at=NULL,
			       updated_at=NOW()
			 WHERE id=$1 AND state='sending' AND provider_message_id IS NULL`,
			rep.JobID, rep.ProviderMessageID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// Raced to sent/requeued by another path; treat as already handled (no double send).
			return errNoop
		}
		// daily_sent_count is NOT touched here (see the doc comment above) — the atomic consume
		// already incremented it. last_success_at is a health-tracking timestamp, not part of the
		// double-booking invariant, so it is still updated on accept.
		if _, err := tx.Exec(ctx,
			`UPDATE sender.accounts a
			   SET last_success_at = NOW(),
			       updated_at = NOW()
			 FROM sender.jobs j
			 WHERE j.id = $1 AND a.id = j.assigned_account_id`, rep.JobID); err != nil {
			return err
		}
		return h.writeSendLog(ctx, tx, rep.JobID, rep.SMTPResponse)
	})
	if err != nil && !errors.Is(err, errNoop) {
		log.Printf("outcome: apply accepted for job %s failed: %v", rep.JobID, err)
	}
}

// applyBounced: sending → bounced (terminal). Sets completed_at, clears the lease (§5.4). Also
// writes a send_logs row for the attempt.
func (h *OutcomeHandler) applyBounced(ctx context.Context, boxID string, rep boxproto.Report) {
	err := h.inActorTx(ctx, "box", func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE sender.jobs
			   SET state='bounced',
			       completed_at=NOW(),
			       lease_expires_at=NULL,
			       last_error_class='unknown',
			       last_error_message=$2,
			       updated_at=NOW()
			 WHERE id=$1 AND state='sending'`, rep.JobID, truncate(rep.SMTPResponse, 2000))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNoop
		}
		return h.writeSendLog(ctx, tx, rep.JobID, rep.SMTPResponse)
	})
	if err != nil && !errors.Is(err, errNoop) {
		log.Printf("outcome: apply bounced for job %s failed: %v", rep.JobID, err)
	}
}

// applyError: sending → queued (the one locked failure transition, §5.4). error_class='unknown'
// (no taxonomy in Phase 3), last_error_message = truncated smtp_response/error_reason, lease
// cleared, attempt_count++, next_attempt_at=NOW(). No auto re-dispatch. Also logs the attempt.
func (h *OutcomeHandler) applyError(ctx context.Context, boxID string, rep boxproto.Report) {
	detail := rep.ErrorReason
	if rep.SMTPResponse != "" {
		detail = rep.ErrorReason + ": " + rep.SMTPResponse
	}
	err := h.inActorTx(ctx, "box", func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE sender.jobs
			   SET state='queued',
			       lease_expires_at=NULL,
			       last_error_class='unknown',
			       last_error_message=$2,
			       attempt_count = attempt_count + 1,
			       next_attempt_at = NOW(),
			       updated_at=NOW()
			 WHERE id=$1 AND state='sending'`, rep.JobID, truncate(detail, 2000))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNoop
		}
		return h.writeSendLog(ctx, tx, rep.JobID, rep.SMTPResponse)
	})
	if err != nil && !errors.Is(err, errNoop) {
		log.Printf("outcome: apply error for job %s failed: %v", rep.JobID, err)
	}
}

// writeSendLog inserts one send_logs row from the job's denormalized content + the report's
// smtp_response (§7). headers are the §8 set the Brain built (re-derived here from the job +
// the account it used, so send_logs.headers matches the bytes sent). ttl_expires_at is
// created_at + debug_log_retention_days. The credential is NEVER written here (§5.5).
func (h *OutcomeHandler) writeSendLog(ctx context.Context, tx pgx.Tx, jobID, smtpResponse string) error {
	// Re-derive the sent headers from the job + its assigned account so send_logs.headers ==
	// the guaranteed §8 set. Message-ID is deterministic from job_id (§8.1 / P8-5), so it matches
	// what the box was handed. Date is send-time; send_logs records the attempt time — acceptable
	// (the header Date lives in the message; send_logs is a debug record, Phase 1 #28).
	var recipient, subject, body string
	var replyTo *string
	var fromAddr string
	var fromName *string
	err := tx.QueryRow(ctx,
		`SELECT j.recipient_email, j.rendered_subject, j.rendered_body, j.reply_to,
		        a.from_address, a.from_name
		 FROM sender.jobs j
		 LEFT JOIN sender.accounts a ON a.id = j.assigned_account_id
		 WHERE j.id = $1`, jobID).Scan(&recipient, &subject, &body, &replyTo, &fromAddr, &fromName)
	if err != nil {
		return err
	}
	fn := ""
	if fromName != nil {
		fn = *fromName
	}
	rt := ""
	if replyTo != nil {
		rt = *replyTo
	}
	headers := buildHeaders(fn, fromAddr, recipient, subject, rt, messageIDFor(jobID, fromAddr), time.Now().UTC())

	_, err = tx.Exec(ctx,
		`INSERT INTO sender.send_logs
		   (job_id, user_id, recipient_email, subject, body, headers, smtp_response, ttl_expires_at)
		 SELECT j.id, j.user_id, j.recipient_email, j.rendered_subject, j.rendered_body,
		        $2::jsonb, $3, NOW() + make_interval(days => $4)
		 FROM sender.jobs j WHERE j.id = $1`,
		jobID, mustJSON(headers), scrubSmtpResponse(smtpResponse), h.cfg.DebugLogRetentionDays)
	return err
}

// jobStatus reports whether the job already has a provider_message_id (already sent), plus its
// current state. A missing job → (false, "", err).
func (h *OutcomeHandler) jobStatus(ctx context.Context, jobID string) (alreadySent bool, state string, err error) {
	var pmid *string
	err = h.db.QueryRow(ctx,
		`SELECT provider_message_id, state::text FROM sender.jobs WHERE id=$1`, jobID).Scan(&pmid, &state)
	if err != nil {
		return false, "", err
	}
	return pmid != nil, state, nil
}

func (h *OutcomeHandler) inActorTx(ctx context.Context, actor string, fn func(tx pgx.Tx) error) error {
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('sender.actor', $1, true)", actor); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// errNoop signals an idempotent no-op (row already handled) so the tx commits cleanly without
// treating a zero-row update as an error.
var errNoop = errors.New("noop: row already in target state")
