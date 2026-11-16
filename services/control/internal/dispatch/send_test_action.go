// send_test_action.go — Module 3 Phase 3 §3.2, C3: "send a test to myself." A REAL send (full
// push_job, MAIL FROM/RCPT TO/DATA), forced to the CLICKED account specifically (M3-P3-OD-16,
// RULED) rather than the cross-account selection a campaign send uses — testing a substitute
// account the user did not ask about answers the wrong question. Named send_test_action.go (not
// send_test.go) because Go treats any _test.go file as test-only and would exclude this from the
// production build.
package dispatch

import (
	"context"
	"fmt"
	"time"

	"github.com/coldmail-send-byo/control/internal/workerproto"
	"github.com/jackc/pgx/v5"
)

// TestSendResult is what a caller (platformapi's test-send handler) receives. Deliberately thin —
// the real outcome (state/provider_message_id/error) lives in send_byo.jobs, exactly like every
// other job; the caller reads it back the same way /v1/report does, rather than this function
// inventing a parallel result shape.
type TestSendResult struct {
	JobID string
}

// ErrAccountIneligibleForTestSend is returned when the CLICKED account specifically is not
// eligible right now (quarantined/disabled/deleted, or paused at its daily limit with no
// self-clear available). The caller must surface this as a clean refusal, never substitute a
// different account (M3-P3-OD-16).
var ErrAccountIneligibleForTestSend = fmt.Errorf("account not eligible for test-send")

// ErrNoWorkerForTestSend is returned when no worker is currently connected to push to.
var ErrNoWorkerForTestSend = fmt.Errorf("no connected worker available for test-send")

// SendTest runs one real send FROM accountID TO recipient, scoped to userID for tenancy. It writes
// a real send_byo.jobs row (with a module-created public.job_items row, M3-P3-OD-17's narrow INSERT
// grant) and pushes over the SAME worker channel and push_job frame every campaign send uses — this
// is not a parallel send path, only a different account-selection step (accountConsumeSpecific
// instead of the cross-account selectAccountForJob). The job's outcome resolves through the SAME
// OutcomeHandler.Handle every other job's report does; SendTest itself does not wait for it —
// §3.2's "it writes a real job row" is satisfied by returning the job id, and the caller polls
// /v1/report exactly as it would for any other async send.
func (d *Dispatcher) SendTest(ctx context.Context, userID, accountID, recipient string, cfg Config) (TestSendResult, error) {
	// The P2 close-out (owner ruling C3): a suspended user's test-send is refused FIRST — before the
	// worker lookup, the account consume, the decrypt, the job_items/jobs writes and the push — so no
	// SMTP session is ever opened on a suspended user's behalf. Fail closed on an unreadable flag.
	// See suspension_gate.go.
	if suspended, serr := userSuspended(ctx, d.db, userID); serr != nil {
		return TestSendResult{}, fmt.Errorf("suspension check: %w", serr)
	} else if suspended {
		return TestSendResult{}, ErrUserSuspended
	}

	workerID := d.px.AnyConnectedWorker()
	if workerID == "" {
		return TestSendResult{}, ErrNoWorkerForTestSend
	}

	// Force the CLICKED account (M3-P3-OD-16): the SAME atomic consume every campaign send uses,
	// scoped to exactly one account id. found=false means THAT account is not eligible right now —
	// fail outright, never fall through to a different account.
	acct, ok, err := d.accountConsumeSpecific(ctx, userID, accountID, cfg.DailyResetTimezone, cfg.DailyResetHour, cfg.ThrottlePauseSeconds)
	if err != nil {
		return TestSendResult{}, err
	}
	if !ok {
		return TestSendResult{}, ErrAccountIneligibleForTestSend
	}

	if bad := validateNoInjection("", acct.fromAddress, recipient, testSendSubject, ""); bad != "" {
		return TestSendResult{}, fmt.Errorf("recipient/from_address carries an injection-shaped byte: %s", bad)
	}

	jobID, err := d.createAndPushTestSend(ctx, userID, acct, recipient, workerID, cfg)
	if err != nil {
		return TestSendResult{}, err
	}
	return TestSendResult{JobID: jobID}, nil
}

const testSendSubject = "Test message from your account"
const testSendBody = "<p>This is a test message sent from your own account to confirm it can deliver mail.</p>"

// createAndPushTestSend does the actual DB write + push: a module-created public.job_items row
// (M3-P3-OD-17's grant), a send_byo.jobs row born DIRECTLY in 'assigned' (skipping 'queued' — there
// is nothing to claim; the account is already forced above, §3.2), then the SAME markSending +
// push_job path resolveAndPush uses for a normal claimed job.
func (d *Dispatcher) createAndPushTestSend(ctx context.Context, userID string, acct selectedAccount, recipient string, workerID string, cfg Config) (string, error) {
	var acctUUID [16]byte
	if err := parseUUID(acct.id, &acctUUID); err != nil {
		return "", fmt.Errorf("parse account id: %w", err)
	}
	plaintext, err := d.dec.Decrypt(acctUUID, acct.keyID, acct.ciphertext, acct.nonce)
	if err != nil {
		return "", fmt.Errorf("decrypt failed for account %s: %w", acct.id, err)
	}
	defer zero(plaintext)

	ctx2, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var jobItemID, jobID string
	err = d.inActorTx(ctx2, "system", func(tx pgx.Tx) error {
		// M3-P3-OD-17's narrow INSERT-only grant: a genuine job_item row, the same shape a
		// platform-initiated single send targeting this account would itself produce.
		if err := tx.QueryRow(ctx2,
			`INSERT INTO public.job_items (campaign_id, user_id, recipient_email, rendered_subject, rendered_body, status)
			 VALUES ($1, $1, $2, $3, $4, 'pending') RETURNING id`,
			userID, recipient, testSendSubject, testSendBody).Scan(&jobItemID); err != nil {
			return fmt.Errorf("create job_item: %w", err)
		}
		// The genesis event's own CHECK (job_events_from_state_check) requires
		// "from_state IS NULL AND to_state = 'queued'" — every job MUST be born queued, with NO
		// exception for a pre-selected account. So this is a genuine two-step birth: INSERT as
		// 'queued' (the real genesis event fires, actor='system'), then an immediate follow-up
		// UPDATE to 'assigned' with the account/worker/lease already forced (the account was already
		// consumed above; nothing else could claim this brand-new row in between, and there is no
		// contention to guard against — it does not exist to any other transaction yet).
		if err := tx.QueryRow(ctx2,
			`INSERT INTO send_byo.jobs
			   (job_item_id, campaign_id, user_id, recipient_email, rendered_subject, rendered_body,
			    state, next_attempt_at)
			 VALUES ($1, $2, $2, $3, $4, $5, 'queued', NOW())
			 RETURNING id`,
			jobItemID, userID, recipient, testSendSubject, testSendBody).Scan(&jobID); err != nil {
			return fmt.Errorf("create job: %w", err)
		}
		if _, err := tx.Exec(ctx2,
			`UPDATE send_byo.jobs
			    SET state = 'assigned', assigned_account_id = $2, assigned_box_id = $3,
			        lease_expires_at = NOW() + make_interval(secs => $4), updated_at = NOW()
			  WHERE id = $1`,
			jobID, acct.id, workerID, cfg.JobMaxProcessingTimeSec); err != nil {
			return fmt.Errorf("assign job: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	sentAt := time.Now().UTC()
	fromName := ""
	if acct.fromName != nil {
		fromName = *acct.fromName
	}
	msgID := messageIDFor(jobID, acct.fromAddress)
	headers := buildHeaders(fromName, acct.fromAddress, recipient, testSendSubject, "", msgID, sentAt)

	moved, token, err := d.markSending(ctx2, jobID, acct.id, workerID)
	if err != nil || !moved {
		zero(plaintext)
		d.requeuePushFailed(ctx2, jobID)
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("test-send job %s did not transition to sending (state drifted)", jobID)
	}

	pj := workerproto.PushJob{
		Type:         workerproto.TypePushJob,
		RequestID:    newRequestID(),
		JobID:        jobID,
		AttemptToken: token,
		Send: workerproto.Send{
			Recipient:   recipient,
			FromAddress: acct.fromAddress,
			FromName:    fromName,
			Subject:     testSendSubject,
			Body:        testSendBody,
			Headers:     headers,
		},
		SMTP: workerproto.SMTP{
			Host:                acct.smtpHost,
			Port:                acct.smtpPort,
			Username:            acct.smtpUser,
			Secret:              string(plaintext),
			Security:            securityForPort(acct.smtpPort),
			OperationTimeoutSec: cfg.SMTPOperationTimeoutSec,
		},
	}
	if lease, ok := d.readLease(ctx2, jobID); ok {
		pj.LeaseExpiresAt = lease.UTC().Format(time.RFC3339)
	}

	err = d.px.PushJob(pj, workerID)
	zero(plaintext)
	pj.SMTP.Secret = ""
	if err != nil {
		d.requeuePushFailed(ctx2, jobID)
		return "", fmt.Errorf("push_job failed for test-send job %s to worker %s: %w", jobID, workerID, err)
	}
	return jobID, nil
}
