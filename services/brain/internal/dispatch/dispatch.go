package dispatch

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sender-module/brain/internal/boxproto"
	"github.com/sender-module/brain/internal/credential"
)

// Pusher is what the dispatcher needs from the box channel: deliver one push_job to a named box,
// and find a connected idle box. Satisfied by boxlink.StreamServer.
type Pusher interface {
	PushJob(pj boxproto.PushJob, boxID string) error
	AnyConnectedBox() string
}

// Dispatcher runs the send path: claims jobs (TryDispatch for Phase 3, Tick for Phase 4's polling
// loop), resolves an eligible account per the job's pool chain and routing strategy, decrypts the
// credential in memory, builds the §8 headers, and pushes one push_job to a connected box.
type Dispatcher struct {
	db  *pgxpool.Pool
	dec *credential.Decryptor
	px  Pusher
	cfg Config

	// rrMu/rrCursor is the in-memory per-pool round_robin account cursor (§3.1, P4-6): soft state,
	// re-derived (reset to empty) on Brain restart — an accepted ≤one-rotation transient skew
	// (§5.3), never load-bearing for correctness (SKIP LOCKED + accounts_daily_sent_check are).
	rrMu     sync.Mutex
	rrCursor map[string]string // pool_id -> last-consumed account id

	// fairMu/fairCursor is the in-memory campaign (≡ user, §8) fairness cursor (§5.1, P4-6): the
	// user_id serviced LAST. Soft state, resets to "" (unset) on Brain restart — the same accepted
	// ≤one-rotation transient skew (§5.3) as rrCursor; no-starvation is structural (every ring
	// member is visited every tick, §5.2), so a lost cursor never starves a campaign, only
	// momentarily re-orders send interleaving.
	fairMu     sync.Mutex
	fairCursor string

	// diagMu/lastSharedPoolNotConfiguredLog throttles the §4.3 shared_pool_not_configured internal
	// diagnostic: an operational condition (not a per-job error), logged at most once per
	// sharedPoolNotConfiguredLogInterval regardless of how many jobs/ticks hit the empty-chain
	// state, so a sustained misconfiguration never floods the log (spec §4.3, test T10 assertion 5).
	diagMu                         sync.Mutex
	lastSharedPoolNotConfiguredLog time.Time
}

// sharedPoolNotConfiguredLogInterval bounds how often the §4.3 diagnostic repeats. Not a
// config-not-code violation: it governs an internal LOG line's own noise floor, not send/dispatch
// behavior — the same category of exemption as maxFallbackHops (§10 P4-19).
const sharedPoolNotConfiguredLogInterval = 1 * time.Minute

// NewDispatcher wires the send path. dec is the SOLE decryptor (Brain-only); it is never handed
// to the admin surface.
func NewDispatcher(db *pgxpool.Pool, dec *credential.Decryptor, px Pusher, cfg Config) *Dispatcher {
	return &Dispatcher{db: db, dec: dec, px: px, cfg: cfg, rrCursor: make(map[string]string)}
}

// logSharedPoolNotConfigured emits the §4.3 internal diagnostic, throttled to at most once per
// sharedPoolNotConfiguredLogInterval across the WHOLE process (not per-user/per-job) — a sustained
// misconfiguration affecting many users/jobs across many ticks still produces only one log line
// per interval, never floods.
func (d *Dispatcher) logSharedPoolNotConfigured() {
	d.diagMu.Lock()
	defer d.diagMu.Unlock()
	if time.Since(d.lastSharedPoolNotConfiguredLog) < sharedPoolNotConfiguredLogInterval {
		return
	}
	d.lastSharedPoolNotConfiguredLog = time.Now()
	log.Printf("dispatch: shared_pool_not_configured — default_shared_pool_id is unset or does not resolve to an enabled shared pool; affected base/fallback campaigns stay queued cleanly until an operator sets it (§4.3)")
}

// claimQuery is the LOCKED Phase 1 claim CTE (§5.1), LIMIT 1 in Phase 3. FOR UPDATE SKIP LOCKED
// => exactly one claimer wins the row. It sets the lease from job_max_processing_time_sec and the
// assigned box. The actor GUC ('dispatcher') must be set in the same tx (the trigger enforces it).
const claimQuery = `
WITH claimed AS (
    SELECT id FROM sender.jobs
    WHERE state = 'queued' AND next_attempt_at <= NOW()
      AND provider_message_id IS NULL
    ORDER BY next_attempt_at ASC, created_at ASC
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE sender.jobs
SET state = 'assigned',
    assigned_box_id = $1,
    lease_expires_at = NOW() + make_interval(secs => $2),
    updated_at = NOW()
WHERE id IN (SELECT id FROM claimed)
RETURNING id, user_id, campaign_id, recipient_email, rendered_subject, rendered_body, reply_to`

// claimedJob is the row the claim returns.
type claimedJob struct {
	id         string
	userID     string
	campaignID string
	recipient  string
	subject    string
	body       string
	replyTo    *string
}

// selectedAccount is the trivially-selected sending account for a job's pool (§5.1).
type selectedAccount struct {
	id          string
	smtpHost    string
	smtpPort    int
	smtpUser    string
	fromAddress string
	fromName    *string
	ciphertext  []byte
	nonce       []byte
	keyID       string
}

// TryDispatch attempts to turn ONE queued job into ONE push_job. It is the Phase-3 single-shot
// primitive, retained for its behavioral tests; the PRODUCTION driver is the Phase-4 polling loop
// (see loop.go / Runner). It is safe to call on every run kickoff; if there is no queued job, no
// connected box, or no eligible account, it is a clean no-op (the job stays claimable). Returns
// the job id it pushed (or "" if nothing was pushed).
func (d *Dispatcher) TryDispatch(ctx context.Context) (string, error) {
	// 0) Need a connected, idle box to push to (boxes are interchangeable — §2.2). If none, do
	// not claim (leaving the job queued for a later kick).
	boxID := d.px.AnyConnectedBox()
	if boxID == "" {
		return "", nil
	}

	// 1) Claim one queued job (locked CTE) + set the assigned box, in a tx with the actor GUC.
	job, ok, err := d.claimOne(ctx, boxID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil // nothing queued
	}

	// 2–5) Resolve the account, decrypt, build headers, transition, and push (shared with the
	// Phase-4 loop). Returns (pushed, err); a clean requeue is (false, nil).
	pushed, err := d.resolveAndPush(ctx, job, boxID)
	if err != nil {
		return "", err
	}
	if pushed {
		return job.id, nil
	}
	return "", nil
}

// resolveAndPush turns ONE already-claimed (state='assigned') job into a push_job on boxID: select
// an account (trivial in Phase 3 / Chunk 1; the atomic consume lands in Chunk 2), decrypt the
// credential in Brain memory, build the §8 headers, transition assigned→sending, and push. On any
// failure it requeues the job to a legal claimable state (lease cleared) and never leaks the
// credential — the plaintext is zeroed on every path. Returns (pushed, err): a clean requeue that
// pushed nothing is (false, nil). This is the per-job handoff shared by TryDispatch (Phase 3) and
// the tick loop (Phase 4, §1.3 RESOLVE_AND_PUSH).
func (d *Dispatcher) resolveAndPush(ctx context.Context, job claimedJob, boxID string) (bool, error) {
	// 2) Resolve the job's pool chain (Chunk 4 wires dedicated-first/shared-fallback; Chunks 1-3
	// use the trivial single-pool chain below) and atomically CONSUME an eligible account from the
	// first pool that has one (§3/§7, THE double-booking core — accountConsume increments
	// daily_sent_count in the SAME statement that admits the row, before any send is attempted).
	// On exhaustion of the whole chain, requeue the claim so the job returns to queued in a legal
	// state (lease cleared) — the rare-race backstop (§7.2); it is not expected in steady state
	// once the Chunk 5 pre-claim gate lands.
	acct, ok, err := d.selectAccountForJob(ctx, job)
	if err != nil || !ok {
		d.requeueNoAccount(ctx, job.id)
		if err != nil {
			return false, err
		}
		return false, nil
	}

	// 3) Decrypt the credential IN BRAIN MEMORY (sole decryptor). AAD = account.id ‖ key_id.
	var acctUUID [16]byte
	if err := parseUUID(acct.id, &acctUUID); err != nil {
		d.requeueNoAccount(ctx, job.id)
		return false, fmt.Errorf("parse account id: %w", err)
	}
	plaintext, err := d.dec.Decrypt(acctUUID, acct.keyID, acct.ciphertext, acct.nonce)
	if err != nil {
		// Cannot decrypt (rotation/tamper): requeue, do not push. Never log the ciphertext.
		log.Printf("dispatch: decrypt failed for account %s (job %s): %v — requeued", acct.id, job.id, err)
		d.requeueNoAccount(ctx, job.id)
		return false, nil
	}

	// 4) Build §8 headers + Message-ID/Date; assemble push_job.
	sentAt := time.Now().UTC()
	fromName := ""
	if acct.fromName != nil {
		fromName = *acct.fromName
	}
	replyTo := ""
	if job.replyTo != nil {
		replyTo = *job.replyTo
	}

	// SMTP header/body-injection guard (§8.1): reject a job whose header-bound values or envelope
	// recipient/from carry CR/LF/NUL rather than compose poisoned DATA bytes. The Brain owns the
	// header set, so this is the choke point. Zero the just-decrypted plaintext and requeue — the
	// credential is never pushed for a poisoned job.
	if bad := validateNoInjection(fromName, acct.fromAddress, job.recipient, job.subject, replyTo); bad != "" {
		zero(plaintext)
		log.Printf("dispatch: job %s has CR/LF in %q — requeued, not sent (header-injection guard)", job.id, bad)
		d.requeue(ctx, job.id, "header_injection:"+bad)
		return false, nil
	}

	msgID := messageIDFor(job.id, acct.fromAddress)
	headers := buildHeaders(fromName, acct.fromAddress, job.recipient, job.subject, replyTo, msgID, sentAt)

	pj := boxproto.PushJob{
		Type:      boxproto.TypePushJob,
		RequestID: newRequestID(),
		JobID:     job.id,
		Send: boxproto.Send{
			Recipient:   job.recipient,
			FromAddress: acct.fromAddress,
			FromName:    fromName,
			ReplyTo:     replyTo,
			Subject:     job.subject,
			Body:        job.body,
			Headers:     headers,
		},
		SMTP: boxproto.SMTP{
			Host:     acct.smtpHost,
			Port:     acct.smtpPort,
			Username: acct.smtpUser,
			Secret:   string(plaintext),
			Security: securityForPort(acct.smtpPort),
		},
	}

	// 5) Move assigned → sending and set assigned_account_id + assigned_box_id, THEN push. Ordering:
	// mark sending first so the state reflects in-flight; if the push fails we requeue (clearing the
	// lease). On a DB error OR a 0-row update (state drifted out of 'assigned'), do NOT push —
	// requeue to a claimable state so the job is never orphaned in 'assigned' with a live lease
	// (the reclaim loop is Phase 6). The credential is zeroed and never leaves the Brain for a job
	// we couldn't transition.
	moved, err := d.markSending(ctx, job.id, acct.id, boxID)
	if err != nil || !moved {
		zero(plaintext)
		d.requeuePushFailed(ctx, job.id)
		if err != nil {
			log.Printf("dispatch: markSending failed for job %s: %v — requeued", job.id, err)
		}
		return false, err
	}
	// Echo the lease so the box knows its deadline (re-read the just-set lease).
	if lease, ok := d.readLease(ctx, job.id); ok {
		pj.LeaseExpiresAt = lease.UTC().Format(time.RFC3339)
	}

	err = d.px.PushJob(pj, boxID)
	// Zero the Brain's plaintext copy immediately after writing it to the stream (§3.3), and scrub
	// it from the local push_job struct so no plaintext lingers in memory beyond the send.
	// RESIDUAL (accepted, defense-in-depth only): string(plaintext) assigned to pj.SMTP.Secret and
	// the JSON-encoded frame in writeFrame are immutable copies that only GC can reclaim — Go
	// cannot zero a string in place. The spec's actual requirements (never logged/persisted/
	// serialized-to-a-client) are met; eliminating this residual would require byte-slice framing
	// end-to-end, out of scope for the JSON wire.
	zero(plaintext)
	pj.SMTP.Secret = ""
	if err != nil {
		// Push failed (box vanished): requeue to a legal state; the box never got the credential.
		log.Printf("dispatch: push_job failed for job %s to box %s: %v — requeued", job.id, boxID, err)
		d.requeuePushFailed(ctx, job.id)
		return false, nil
	}
	return true, nil
}

// claimOne runs the locked claim CTE with the actor GUC set (same tx).
func (d *Dispatcher) claimOne(ctx context.Context, boxID string) (claimedJob, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := d.db.Begin(ctx)
	if err != nil {
		return claimedJob{}, false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('sender.actor', 'dispatcher', true)"); err != nil {
		return claimedJob{}, false, err
	}
	var j claimedJob
	err = tx.QueryRow(ctx, claimQuery, boxID, d.cfg.JobMaxProcessingTimeSec).
		Scan(&j.id, &j.userID, &j.campaignID, &j.recipient, &j.subject, &j.body, &j.replyTo)
	if err == pgx.ErrNoRows {
		return claimedJob{}, false, tx.Commit(ctx) // nothing queued; commit the (no-op) tx
	}
	if err != nil {
		return claimedJob{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return claimedJob{}, false, err
	}
	return j, true, nil
}

// markSending moves assigned → sending and records the account + the box, in a tx with the actor
// GUC. The lease stays set (still in {assigned,sending} — CHECK satisfied). Box selection is
// orthogonal to routing (§1.3), so the box is recorded here at the sending transition rather than
// at claim. Returns (moved, err): moved is false when the guarded UPDATE affected 0 rows (the job
// was not in 'assigned' — state drifted), so the caller must NOT push and should requeue rather
// than send on a job it didn't transition.
func (d *Dispatcher) markSending(ctx context.Context, jobID, accountID, boxID string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	moved := false
	err := d.inActorTx(ctx, "dispatcher", func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE sender.jobs SET state='sending', assigned_account_id=$2, assigned_box_id=$3, updated_at=NOW()
			 WHERE id=$1 AND state='assigned'`, jobID, accountID, boxID)
		if err != nil {
			return err
		}
		moved = tag.RowsAffected() == 1
		return nil
	})
	return moved, err
}

// requeueNoAccount / requeuePushFailed return an assigned/sending job to queued in a LEGAL state
// (lease cleared — §5.2), with attempt_count++. No auto re-dispatch (Phase 3, §5.4).
func (d *Dispatcher) requeueNoAccount(ctx context.Context, jobID string) {
	d.requeue(ctx, jobID, "no_eligible_account")
}
func (d *Dispatcher) requeuePushFailed(ctx context.Context, jobID string) {
	d.requeue(ctx, jobID, "push_failed")
}

func (d *Dispatcher) requeue(ctx context.Context, jobID, reason string) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := d.inActorTx(ctx, "dispatcher", func(tx pgx.Tx) error {
		// Move back to queued from assigned OR sending; clear the lease (required by the CHECK).
		_, err := tx.Exec(ctx,
			`UPDATE sender.jobs
			   SET state='queued',
			       assigned_box_id=NULL,
			       lease_expires_at=NULL,
			       last_error_class='unknown',
			       last_error_message=$2,
			       attempt_count = attempt_count + 1,
			       next_attempt_at = NOW(),
			       updated_at=NOW()
			 WHERE id=$1 AND state IN ('assigned','sending')`, jobID, truncate(reason, 2000))
		return err
	})
	if err != nil {
		log.Printf("dispatch: requeue(%s) job %s failed: %v", reason, jobID, err)
	}
}

func (d *Dispatcher) readLease(ctx context.Context, jobID string) (time.Time, bool) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var t *time.Time
	if err := d.db.QueryRow(ctx, `SELECT lease_expires_at FROM sender.jobs WHERE id=$1`, jobID).Scan(&t); err != nil || t == nil {
		return time.Time{}, false
	}
	return *t, true
}

// inActorTx runs fn inside a tx with the actor GUC set (so the state-change trigger fires).
func (d *Dispatcher) inActorTx(ctx context.Context, actor string, fn func(tx pgx.Tx) error) error {
	tx, err := d.db.Begin(ctx)
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

// securityForPort derives the SMTP security mode from the port (§4.2): 465 = implicit TLS,
// everything else (587/25) = STARTTLS.
func securityForPort(port int) string {
	if port == 465 {
		return boxproto.SecurityImplicitTLS
	}
	return boxproto.SecurityStartTLS
}

// truncate caps s to n bytes (last_error_message CHECK ≤ 2000).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// parseUUID parses a canonical UUID string into its raw 16 bytes (for the AAD). It accepts the
// hyphenated form only.
func parseUUID(s string, out *[16]byte) error {
	h := strings.ReplaceAll(s, "-", "")
	if len(h) != 32 {
		return fmt.Errorf("bad uuid length")
	}
	b, err := hex.DecodeString(h)
	if err != nil {
		return err
	}
	copy(out[:], b)
	return nil
}

// zero overwrites a plaintext byte slice (the Brain's transient credential copy — §3.3).
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
