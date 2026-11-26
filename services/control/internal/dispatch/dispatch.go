package dispatch

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coldmail-send-byo/control/internal/credential"
	"github.com/coldmail-send-byo/control/internal/workerproto"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pusher is what the dispatcher needs from the worker channel: deliver one push_job to a named worker,
// and find a connected idle worker. Satisfied by workerlink.StreamServer.
type Pusher interface {
	PushJob(pj workerproto.PushJob, workerID string) error
	AnyConnectedWorker() string
	// PushVerify delivers one push_verify frame (Module 3 Phase 3 §2.2) to a named worker — same
	// channel and same "must be currently registered" precondition as PushJob.
	PushVerify(pv workerproto.PushVerify, workerID string) error
}

// Dispatcher runs the send path: claims jobs (TryDispatch for Phase 3, Tick for Phase 4's polling
// loop), resolves an eligible account from the job's user per the routing strategy, decrypts the
// credential in memory, builds the §8 headers, and pushes one push_job to a connected worker.
type Dispatcher struct {
	db  *pgxpool.Pool
	dec *credential.Decryptor
	px  Pusher

	// rrMu/rrCursor is the in-memory per-user round_robin account cursor (§3.1, P4-6; re-keyed from
	// per-pool to per-user in G3 — Docs/history/phase-2-strip-shared-pool.md §3.4): soft state, re-derived
	// (reset to empty) on Control restart — an accepted ≤one-rotation transient skew (§5.3), never
	// load-bearing for correctness (SKIP LOCKED + accounts_daily_sent_check are).
	rrMu     sync.Mutex
	rrCursor map[string]string // user_id -> last-consumed account id

	// fairMu/fairCursor is the in-memory campaign (≡ user, §8) fairness cursor (§5.1, P4-6): the
	// user_id serviced LAST. Soft state, resets to "" (unset) on Control restart — the same accepted
	// ≤one-rotation transient skew (§5.3) as rrCursor; no-starvation is structural (every ring
	// member is visited every tick, §5.2), so a lost cursor never starves a campaign, only
	// momentarily re-orders send interleaving.
	fairMu     sync.Mutex
	fairCursor string

	// pace is the Phase 6 per-lane inter-send pacing register (§1, P6-1): soft, in-memory
	// next_send_at timestamps per user's lanes. It decides only WHEN a lane may fire (never how
	// many are in flight — that stays DB-derived, §1.1), and is consulted off the tick's critical
	// path (a timestamp read, never a sleep). Like the cursors above, it resets on Control restart —
	// the accepted one-time unpaced burst (§1.1), never load-bearing for correctness.
	pace *pacer

	// health is the Phase 7 §2 outage gate (P7-3), SHARED by the dispatch loop, the reclaim loop and
	// (Chunk 4) the retention sweep so one detection pauses all three. It holds no authoritative job
	// state — only "is the DB reachable" + the backoff — so a lost gate can never invent an outcome.
	health *DBHealth

	// draining is the Phase 7 §4 graceful-shutdown latch (P7-5): once set (SIGTERM), the dispatcher
	// claims no new job and pushes nothing, so no fresh work enters flight while the drain waits for
	// in-flight sends to report. It is one-way — a shutdown is not resumable — and holds no job state,
	// so it can never cause an invented outcome; the leave-in-place guarantee is structural (drain.go
	// writes no job row at all).
	draining atomic.Bool

	// diagMu guards the throttled internal diagnostics below. lastDispatchPausedLog is the Phase
	// 9.5 B1 diagnostic for dispatch_enabled=false (§1.2). lastNotLeaderLog is the same throttle for
	// the G11-OD-1 / ADR-0011 leadership gate below. The pool-chain-empty diagnostic this struct
	// once also throttled (shared_pool_not_configured) died with pool.go at G1 — its subject,
	// resolvePoolChain's empty-chain branch, no longer exists.
	// lastReclaimFrozenLog is the item-12 diagnostic for the `stopped` rung's frozen reclaim sweep.
	// It is a SEPARATE timestamp from lastDispatchPausedLog on purpose, even though both fire from the
	// same rung: they are emitted by two different loops, and sharing one throttle would let the
	// dispatch loop's line (which ticks far more often) suppress the reclaim loop's for the whole
	// interval. The frozen sweep is the one thing about `stopped` an operator cannot infer from any
	// other line, so it is exactly the line that must not be swallowed.
	diagMu                sync.Mutex
	lastDispatchPausedLog time.Time
	lastReclaimFrozenLog  time.Time
	lastNotLeaderLog      time.Time

	// leader is the G11-OD-1 / ADR-0011 (option B, ruled 2026-07-29) LEADERSHIP gate: "do I still
	// hold the singleton advisory lock?". Wired from main.go to singleton.Guard.Healthy. While it
	// answers false the dispatch, reclaim and retention loops PAUSE — exactly as they pause on a DB
	// outage, and for the same reason (Rule 7: one dispatcher or none).
	//
	// WHY THIS EXISTS AS A SEPARATE GATE from DBHealth even though both pause the same three loops:
	// they are different questions with different answers. DBHealth asks "can I reach the DB" and a
	// halted DBHealth means nothing CAN be written; leader asks "am I entitled to dispatch" and a
	// false leader means the DB is perfectly reachable and writes would succeed — which is precisely
	// the danger. Folding leadership into DBHealth would make a lockless Control look like an outage,
	// muddling both the log line and readyz.
	//
	// nil (never wired) => leadership is not checked, preserving behavior for callers that construct
	// a Dispatcher without a Guard (every dispatch-package test does). Those tests hold no lock and
	// must not be gated by one; the real Control always wires it in main.go.
	leader atomic.Pointer[func() bool]
}

// SetLeadership wires the singleton-lock gate (G11-OD-1 / ADR-0011). Pass singleton.Guard.Healthy.
// Called once at startup before the loops run; safe at any time (read atomically per tick, so an
// operator-visible change takes effect on the next tick, no redeploy).
func (d *Dispatcher) SetLeadership(fn func(context.Context) bool) {
	if fn == nil {
		d.leader.Store(nil)
		return
	}
	f := func() bool { return fn(context.Background()) }
	d.leader.Store(&f)
}

// IsLeader reports whether this Control still holds the singleton advisory lock. True when no
// leadership gate is wired (see the leader field's comment: an un-wired Dispatcher is a test
// Dispatcher, which holds no lock and must not be gated by one).
func (d *Dispatcher) IsLeader() bool {
	fn := d.leader.Load()
	if fn == nil {
		return true
	}
	return (*fn)()
}

// logNotLeader emits the ADR-0011 pause diagnostic, throttled across the whole process to at most
// once per notLeaderLogInterval (same shape as the §4.3 and B1 diagnostics) so a Control that sits
// paused for hours produces a bounded trickle, not one line per tick per loop.
func (d *Dispatcher) logNotLeader(loop string) {
	d.diagMu.Lock()
	defer d.diagMu.Unlock()
	if time.Since(d.lastNotLeaderLog) < notLeaderLogInterval {
		return
	}
	d.lastNotLeaderLog = time.Now()
	log.Printf("%s: singleton_lock_not_held — this Control does not hold the singleton advisory lock, so it claimed/routed/pushed/reclaimed/swept NOTHING this tick; queued and in-flight jobs are untouched and no outcome is invented. Another Control may be the active dispatcher. The outcome path stays UP so an in-flight send can still report. Resumes automatically on re-acquire (ADR-0011 option B) level=error", loop)
}

// dispatchPausedLogInterval bounds how often the Phase 9.5 B1 panic-switch diagnostic repeats. Not
// a config-not-code violation: it governs an internal LOG line's own noise floor, not send/dispatch
// behavior — the same category of exemption as maxFallbackHops (§10 P4-19).
const dispatchPausedLogInterval = 1 * time.Minute

// notLeaderLogInterval is the same log-noise-floor exemption for the ADR-0011 leadership gate.
const notLeaderLogInterval = 1 * time.Minute

// NewDispatcher wires the send path. dec is the SOLE decryptor (Control-only); it is never handed
// to the admin surface. There is no config snapshot stored on the Dispatcher itself — every entry
// point (TryDispatch, Tick) loads config fresh at its own start, so a config change is visible on
// the very next attempt with no stale in-memory copy (§0 OD-14).
func NewDispatcher(db *pgxpool.Pool, dec *credential.Decryptor, px Pusher) *Dispatcher {
	return &Dispatcher{db: db, dec: dec, px: px, rrCursor: make(map[string]string), pace: newPacer(), health: NewDBHealth(db)}
}

// Health exposes the Phase 7 §2 outage gate so every loop driven off this Dispatcher (dispatch,
// reclaim, retention) consults the SAME halt state — one detection pauses all of them (§2.2).
func (d *Dispatcher) Health() *DBHealth { return d.health }

// logDispatchPausedByOperator emits the Phase 9.5 B1 internal diagnostic (§1.2), throttled to at
// most once per dispatchPausedLogInterval across the whole process — a sustained operator pause
// produces one log line per interval, never a flood, regardless of how many ticks elapse.
//
// IT NAMES THE RUNG (fix-plan item 12). Since send_byo_0052 there are three rungs at which claiming
// stops, and they differ in what ELSE is still running. A line that said only "dispatch_enabled=false"
// would be actively misleading at refuse_new (where intake is also refused) and at stopped (where the
// reclaim sweep is frozen too) — an operator reading it during an incident would conclude the system
// was in a state it is not. M1's D2 damage was exactly this: everything looked normal while the config
// was ignored, so echoing a stale framing proves nothing. The rung is therefore passed in from the
// gate that fired, and the line reports which lever produced it — the mode, the legacy boolean floor,
// or both.
func (d *Dispatcher) logDispatchPausedByOperator(cfg Config) {
	d.diagMu.Lock()
	defer d.diagMu.Unlock()
	if time.Since(d.lastDispatchPausedLog) < dispatchPausedLogInterval {
		return
	}
	d.lastDispatchPausedLog = time.Now()
	rung := cfg.EffectiveRung()
	switch rung {
	case RungStopped:
		log.Printf("dispatch: dispatch_paused_by_operator — rung=%s (dispatch_mode=%q dispatch_enabled=%t); this tick claimed/routed/pushed nothing, queued jobs are untouched, POST /v1/run is REFUSED 503 paused, and the RECLAIM SWEEP IS FROZEN (an expired lease is left in place until this rung is released); outcome ingestion and heartbeats continue (§1.2, fix-plan item 12)", rung, cfg.DispatchMode, cfg.DispatchEnabled)
	case RungRefuseNew:
		log.Printf("dispatch: dispatch_paused_by_operator — rung=%s (dispatch_mode=%q dispatch_enabled=%t); this tick claimed/routed/pushed nothing, queued jobs are untouched, POST /v1/run is REFUSED 503 paused; reclaim/outcome/heartbeat continue (§1.2, fix-plan item 12)", rung, cfg.DispatchMode, cfg.DispatchEnabled)
	default:
		// accept_and_defer — reachable from dispatch_mode, from dispatch_enabled=false (the severity
		// floor), or from an unrecognized mode. Naming both dials is what tells an operator who pulled
		// only one of them why they are here.
		log.Printf("dispatch: dispatch_paused_by_operator — rung=%s (dispatch_mode=%q dispatch_enabled=%t); this tick claimed/routed/pushed nothing, queued jobs are untouched, POST /v1/run still ACCEPTS and queues, reclaim/outcome/heartbeat continue (§1.2)", rung, cfg.DispatchMode, cfg.DispatchEnabled)
	}
}

// logReclaimFrozenByOperator emits the item-12 diagnostic for the `stopped` rung, throttled on its
// own timestamp (see lastReclaimFrozenLog). Separate from the dispatch line because it reports a
// separate fact: not "nothing was claimed" but "an expired lease was deliberately left in place."
func (d *Dispatcher) logReclaimFrozenByOperator(cfg Config) {
	d.diagMu.Lock()
	defer d.diagMu.Unlock()
	if time.Since(d.lastReclaimFrozenLog) < dispatchPausedLogInterval {
		return
	}
	d.lastReclaimFrozenLog = time.Now()
	log.Printf("reclaim: reclaim_frozen_by_operator — rung=%s (dispatch_mode=%q); this tick reclaimed nothing and wrote nothing: an expired lease or dead worker is left exactly as it is until the rung is released. Nothing is stranded (both detectors are DB-predicate reads, so they re-fire on the first tick after release) and nothing is lost. Release with dispatch_mode=refuse_new or lower to resume the sweep (fix-plan item 12)", cfg.EffectiveRung(), cfg.DispatchMode)
}

// claimQuery is the LOCKED Phase 1 claim CTE (§5.1), LIMIT 1 in Phase 3. FOR UPDATE SKIP LOCKED
// => exactly one claimer wins the row. It sets the lease from job_max_processing_time_sec and the
// assigned worker. The actor GUC ('dispatcher') must be set in the same tx (the trigger enforces it).
// notSuspendedFragment (Module 3 Phase 5 §6.3) is included here too — the same reasoning as
// claimForUserQuery's own comment: TryDispatch is a second, independent claim path, and "a filter
// is not a gate" means the suspend predicate belongs in every statement that can claim a job, not
// only in the live loop's ring-membership filter. notPausedFragment (Phase 4 C1: a campaign the platform paused
// with POST /v1/pause) is stated here for the identical reason, as the same shared constant.
const claimQuery = `
WITH claimed AS (
    SELECT id FROM send_byo.jobs j
    WHERE j.state = 'queued' AND j.next_attempt_at <= NOW()
      AND j.provider_message_id IS NULL
      AND j.cancel_requested_at IS NULL
      AND ` + notSuspendedFragment + `
      AND ` + notPausedFragment + `
    ORDER BY j.next_attempt_at ASC, j.created_at ASC
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE send_byo.jobs
SET state = 'assigned',
    assigned_box_id = $1,
    lease_expires_at = NOW() + make_interval(secs => $2),
    updated_at = NOW()
WHERE id IN (SELECT id FROM claimed)
RETURNING id, user_id, campaign_id, recipient_email, rendered_subject, rendered_body, rendered_text, reply_to`

// claimedJob is the row the claim returns.
type claimedJob struct {
	id         string
	userID     string
	campaignID string
	recipient  string
	subject    string
	body       string
	text       *string // rendered_text: the optional plain-text alternative (NULL when the platform wrote none)
	replyTo    *string
}

// selectedAccount is the selected sending account for a job's user (§5.1).
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
// connected worker, or no eligible account, it is a clean no-op (the job stays claimable). Returns
// the job id it pushed (or "" if nothing was pushed).
func (d *Dispatcher) TryDispatch(ctx context.Context) (string, error) {
	// Load config fresh at entry — the SAME freshness contract Tick gives its own callers (§0 OD-14:
	// a config change takes effect on the very next attempt, no redeploy, no stale in-memory copy).
	cfg, err := LoadConfig(ctx, d.db)
	if err != nil {
		return "", err
	}

	// Phase 7 §4.1 step 1 (P7-5): STOP-CLAIM while draining. Same gate as Tick — the Phase-3 primitive
	// is a second claim path (platform `run` kicks reach the Runner, but this entry point is still
	// live), so it must honor the drain too or a SIGTERM could still put a fresh job into flight.
	if d.draining.Load() {
		return "", nil
	}
	// Phase 9.5 B1 (§1.2) + fix-plan item 12: the same ladder gate Tick honors. TryDispatch is no
	// longer wired into main.go's live loop (the Runner drives Tick), but it remains a public claim
	// path any future caller could invoke directly, so it honors the rung too rather than relying on
	// callers to remember the gate — consistent with the drain check just above it.
	if !cfg.ClaimsAllowed() {
		d.logDispatchPausedByOperator(cfg)
		return "", nil
	}

	// 0) Need a connected, idle worker to push to (workers are interchangeable — §2.2). If none, do
	// not claim (leaving the job queued for a later kick).
	workerID := d.px.AnyConnectedWorker()
	if workerID == "" {
		return "", nil
	}

	// 1) Claim one queued job (locked CTE) + set the assigned worker, in a tx with the actor GUC.
	job, ok, err := d.claimOne(ctx, workerID, cfg.JobMaxProcessingTimeSec)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil // nothing queued
	}

	// 2–5) Resolve the account, decrypt, build headers, transition, and push (shared with the
	// Phase-4 loop). Returns (pushed, err); a clean requeue is (false, nil).
	pushed, err := d.resolveAndPush(ctx, job, workerID, cfg)
	if err != nil {
		return "", err
	}
	if pushed {
		return job.id, nil
	}
	return "", nil
}

// resolveAndPush turns ONE already-claimed (state='assigned') job into a push_job on workerID: select
// an account (trivial in Phase 3 / Chunk 1; the atomic consume lands in Chunk 2), decrypt the
// credential in Control memory, build the §8 headers, transition assigned→sending, and push. On any
// failure it requeues the job to a legal claimable state (lease cleared) and never leaks the
// credential — the plaintext is zeroed on every path. Returns (pushed, err): a clean requeue that
// pushed nothing is (false, nil). This is the per-job handoff shared by TryDispatch (Phase 3) and
// the tick loop (Phase 4, §1.3 RESOLVE_AND_PUSH). cfg is the caller's already-loaded config,
// threaded through to selectAccountForJob so a job-loop of N claimed jobs does not re-read
// send_byo.config N times in a single tick.
func (d *Dispatcher) resolveAndPush(ctx context.Context, job claimedJob, workerID string, cfg Config) (bool, error) {
	// 2) Resolve an eligible account from the job's user and atomically CONSUME it (§3/§7, THE
	// double-booking core — accountConsume increments daily_sent_count in the SAME statement that
	// admits the row, before any send is attempted). On exhaustion of the user's account set,
	// requeue the claim so the job returns to queued in a legal state (lease cleared) — the
	// rare-race backstop (§7.2); it is not expected in steady state once the Chunk 5 pre-claim gate
	// lands.
	acct, ok, err := d.selectAccountForJob(ctx, job, cfg)
	if err != nil || !ok {
		d.requeueNoAccount(ctx, job.id)
		if err != nil {
			return false, err
		}
		return false, nil
	}

	// 3) Decrypt the credential IN CONTROL MEMORY (sole decryptor). AAD = account.id ‖ key_id.
	var acctUUID [16]byte
	if err := parseUUID(acct.id, &acctUUID); err != nil {
		d.requeueNoAccount(ctx, job.id)
		return false, fmt.Errorf("parse account id: %w", err)
	}
	plaintext, err := d.dec.Decrypt(acctUUID, acct.keyID, acct.ciphertext, acct.nonce)
	if err != nil {
		// Cannot decrypt (rotation/tamper): requeue, do not push. Never log the ciphertext.
		log.Printf("dispatch: decrypt failed for account %s (job %s): %v — requeued level=error", acct.id, job.id, err)
		d.requeueNoAccount(ctx, job.id)
		return false, nil
	}
	// Zero on EVERY exit from here on, including a panic (CLAUDE.md Rule 6: "zeroes them on EVERY
	// exit path — success, error, panic, shutdown, timeout"). zero() is idempotent, so the later
	// explicit calls below (kept for clarity at the point of use) are harmless double-zeroing, not
	// a correctness issue — this defer is the actual guarantee.
	defer zero(plaintext)

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
	// recipient/from carry CR/LF/NUL rather than compose poisoned DATA bytes. The Control owns the
	// header set, so this is the choke point. Zero the just-decrypted plaintext and requeue — the
	// credential is never pushed for a poisoned job.
	if bad := validateNoInjection(fromName, acct.fromAddress, job.recipient, job.subject, replyTo); bad != "" {
		zero(plaintext)
		log.Printf("dispatch: job %s has CR/LF in %q — requeued, not sent (header-injection guard) level=warn", job.id, bad)
		d.requeue(ctx, job.id, "header_injection:"+bad)
		return false, nil
	}

	msgID := messageIDFor(job.id, acct.fromAddress)
	headers := buildHeaders(fromName, acct.fromAddress, job.recipient, job.subject, replyTo, msgID, sentAt)

	pj := workerproto.PushJob{
		Type:      workerproto.TypePushJob,
		RequestID: newRequestID(),
		JobID:     job.id,
		Send: workerproto.Send{
			Recipient:   job.recipient,
			FromAddress: acct.fromAddress,
			FromName:    fromName,
			ReplyTo:     replyTo,
			Subject:     job.subject,
			Body:        job.body,
			Text:        sendText(job.text),
			Headers:     headers,
		},
		SMTP: workerproto.SMTP{
			Host:     acct.smtpHost,
			Port:     acct.smtpPort,
			Username: acct.smtpUser,
			Secret:   string(plaintext),
			Security: securityForPort(acct.smtpPort),
			// Deliver the seeded worker per-operation SMTP timeout (§5, P6-11) to the DB-less worker. The
			// Control enforces smtp_operation_timeout_sec < job_max_processing_time_sec at config-load
			// (config.go), so the value the worker receives here is always strictly below its own lease —
			// an account hang trips the worker's operation timeout FIRST and is reported (scored), never a
			// silent Control lease reclaim (P6-10 carve-out's correctness dependency).
			OperationTimeoutSec: cfg.SMTPOperationTimeoutSec,
			// C4: the connect-time destination guard's allowlist (empty in production), see dest_guard.go.
			DestAllowCIDRs: allowCIDRsForWire(cfg.SMTPHostAllowCIDRs),
		},
		// The resolved content-variation snapshot (§2.2, P8-3 GLOBAL scope): cfg's already-loaded
		// send_byo_0020 values, copied verbatim into the wire shape (Chunk 5, config.go/variation.go).
		// A snapshot whose every field is still at its neutral default is byte-identical in effect
		// to omitting the field entirely (§4.2).
		Variation: variationSnapshot(cfg),
	}

	// 5) Move assigned → sending and set assigned_account_id + assigned_box_id, THEN push. Ordering:
	// mark sending first so the state reflects in-flight; if the push fails we requeue (clearing the
	// lease). On a DB error OR a 0-row update (state drifted out of 'assigned'), do NOT push —
	// requeue to a claimable state so the job is never orphaned in 'assigned' with a live lease
	// (the reclaim loop is Phase 6). The credential is zeroed and never leaves the Control for a job
	// we couldn't transition.
	moved, token, err := d.markSending(ctx, job.id, acct.id, workerID)
	if err != nil || !moved {
		zero(plaintext)
		d.requeuePushFailed(ctx, job.id)
		if err != nil {
			log.Printf("dispatch: markSending failed for job %s: %v — requeued level=error", job.id, err)
		}
		return false, err
	}
	// Carry THIS attempt's persisted identity on the wire (Phase 8 Chunk 1, P7-OD-6). The worker echoes it
	// in its report and the Control rejects any report bearing a superseded token. Set AFTER the
	// transition, from the value the UPDATE returned — never guessed, never derived worker-side.
	pj.AttemptToken = token
	// Echo the lease so the worker knows its deadline (re-read the just-set lease).
	if lease, ok := d.readLease(ctx, job.id); ok {
		pj.LeaseExpiresAt = lease.UTC().Format(time.RFC3339)
	}

	err = d.px.PushJob(pj, workerID)
	// Zero the Control's plaintext copy immediately after writing it to the stream (§3.3), and scrub
	// it from the local push_job struct so no plaintext lingers in memory beyond the send.
	// RESIDUAL (accepted, defense-in-depth only): string(plaintext) assigned to pj.SMTP.Secret and
	// the JSON-encoded frame in writeFrame are immutable copies that only GC can reclaim — Go
	// cannot zero a string in place. The spec's actual requirements (never logged/persisted/
	// serialized-to-a-client) are met; eliminating this residual would require byte-slice framing
	// end-to-end, out of scope for the JSON wire.
	zero(plaintext)
	pj.SMTP.Secret = ""
	if err != nil {
		// Push failed (worker vanished): requeue to a legal state; the worker never got the credential.
		log.Printf("dispatch: push_job failed for job %s to worker %s: %v — requeued level=warn", job.id, workerID, err)
		d.requeuePushFailed(ctx, job.id)
		return false, nil
	}
	return true, nil
}

// claimOne runs the locked claim CTE with the actor GUC set (same tx).
func (d *Dispatcher) claimOne(ctx context.Context, workerID string, jobMaxProcessingTimeSec int) (claimedJob, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := d.db.Begin(ctx)
	if err != nil {
		return claimedJob{}, false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('send_byo.actor', 'dispatcher', true)"); err != nil {
		return claimedJob{}, false, err
	}
	var j claimedJob
	err = tx.QueryRow(ctx, claimQuery, workerID, jobMaxProcessingTimeSec).
		Scan(&j.id, &j.userID, &j.campaignID, &j.recipient, &j.subject, &j.body, &j.text, &j.replyTo)
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

// markSending moves assigned → sending and records the account + the worker, in a tx with the actor
// GUC. The lease stays set (still in {assigned,sending} — CHECK satisfied). Worker selection is
// orthogonal to routing (§1.3), so the worker is recorded here at the sending transition rather than
// at claim. Returns (moved, token, err): moved is false when the guarded UPDATE affected 0 rows (the
// job was not in 'assigned' — state drifted), so the caller must NOT push and should requeue rather
// than send on a job it didn't transition.
//
// ATTEMPT IDENTITY (Phase 8 Chunk 1, P7-OD-6). The same guarded UPDATE increments
// attempt_token and RETURNS it. This is the ONE place a token is minted, and it is minted exactly
// where the attempt begins: the update is already guarded by `state='assigned'`, so a job that lost
// its claim mints nothing, and two concurrent dispatchers cannot both mint (the row lock plus the
// state guard admit exactly one). The value is unforgeable by the worker — it never chooses it, it only
// echoes what it was handed — and non-saturating by construction (`+ 1`, no cap), which is exactly
// what attempt_count cannot be (§Phase 5 saturates it at max_attempts, so long-lived jobs reuse it).
//
// LOCK ORDER (Rule 9 / ADR-0010): unchanged by this addition. markSending takes NO exclusive account
// lock — it takes the job row's write lock and then the account's implicit FK KEY SHARE via the
// job_events trigger (state genuinely changes here) and assigned_account_id. Adding attempt_token to
// the same SET list adds no lock and reorders none. Its only concurrent same-job counterparty is the
// claim CTE / reclaim, which acquire the job with FOR UPDATE SKIP LOCKED (skipped, never blocked-on) —
// the documented safe case in Rule 9's precise scope.
func (d *Dispatcher) markSending(ctx context.Context, jobID, accountID, workerID string) (bool, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	moved := false
	var token int64
	err := d.inActorTx(ctx, "dispatcher", func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx,
			`UPDATE send_byo.jobs SET state='sending', assigned_account_id=$2, assigned_box_id=$3,
			        attempt_token = attempt_token + 1, updated_at=NOW()
			 WHERE id=$1 AND state='assigned'
			 RETURNING attempt_token`, jobID, accountID, workerID).Scan(&token)
		if err == pgx.ErrNoRows {
			return nil // state drifted out of 'assigned'; moved stays false, caller requeues
		}
		if err != nil {
			return err
		}
		moved = true
		return nil
	})
	return moved, token, err
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
		// attempt_count SATURATES at max_attempts (Phase 5 §6.2): the pre-existing dispatch-side
		// requeue paths (no_eligible_account / push_failed / header_injection) increment the same
		// counter as the outcome handler, so they must saturate too — an unconditional +1 here would
		// violate jobs_attempt_count_check (23514) once a job that has already hit the cap requeues
		// again (e.g. its only account was auto-quarantined). Job failure is decoupled from
		// attempt_count; only pool exhaustion fails a job (Chunk 5).
		_, err := tx.Exec(ctx,
			`UPDATE send_byo.jobs
			   SET state='queued',
			       assigned_box_id=NULL,
			       lease_expires_at=NULL,
			       last_error_class='unknown',
			       last_error_message=$2,
			       attempt_count = LEAST(attempt_count + 1, max_attempts),
			       next_attempt_at = NOW(),
			       updated_at=NOW()
			 WHERE id=$1 AND state IN ('assigned','sending')`, jobID, truncate(reason, 2000))
		return err
	})
	if err != nil {
		log.Printf("dispatch: requeue(%s) job %s failed: %v level=error", reason, jobID, err)
	}
}

func (d *Dispatcher) readLease(ctx context.Context, jobID string) (time.Time, bool) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var t *time.Time
	if err := d.db.QueryRow(ctx, `SELECT lease_expires_at FROM send_byo.jobs WHERE id=$1`, jobID).Scan(&t); err != nil {
		log.Printf("dispatch: readLease(%s) query failed: %v — pushing without an echoed lease deadline level=error", jobID, err)
		return time.Time{}, false
	}
	if t == nil {
		// A job just marked 'sending' must carry a non-null lease (jobs_lease_valid_check); a NULL
		// read here means the state machine's own invariant was violated somewhere upstream.
		log.Printf("dispatch: readLease(%s) returned a NULL lease on a job just marked sending — state-machine invariant violated level=error", jobID)
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
	if _, err := tx.Exec(ctx, "SELECT set_config('send_byo.actor', $1, true)", actor); err != nil {
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
		return workerproto.SecurityImplicitTLS
	}
	return workerproto.SecurityStartTLS
}

// truncate caps s to n bytes (last_error_message CHECK ≤ 2000).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// nullableTruncate is truncate's NULL-on-empty counterpart for jobs.last_smtp_response (Phase 7
// §1.4): an empty provider response should read as SQL NULL, not an empty string, matching the
// column's own nullability (no response text at all — e.g. a connect_refused/timeout failure that
// never reached the provider — is a genuinely different fact from "the provider replied with
// nothing," and NULL is the honest way to say so).
func nullableTruncate(s string, n int) *string {
	if s == "" {
		return nil
	}
	t := truncate(s, n)
	return &t
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

// zero overwrites a plaintext byte slice (the Control's transient credential copy — §3.3).
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
