// loop.go — the Phase-4 polling dispatch loop (§1.2): one tick snapshots ground truth, computes
// per-user lanes, applies the pre-claim account-eligibility gate (§1.2/§6, P4-5), visits active
// campaigns in fairness-cursor order (§5), claims per active user (§1.1), and turns each claimed
// job into a push_job handoff. This is the PRODUCTION driver; TryDispatch (dispatch.go) is the
// Phase-3 single-shot primitive kept for its own behavioral tests.
package dispatch

import (
	"context"
	"log"
	"sort"
	"time"

	"github.com/coldmail-send-byo/control/internal/userconfig"
)

// notSuspendedFragment is Module 3 Phase 5 §6.3's ONE shared SQL fragment: "a filter is not a
// gate" — snapshotActiveUsers keeps a suspended user out of the LIVE loop's ring, but any OTHER
// caller of a claim path bypasses a filter that lives only there. There are two claim SQL
// constants (claimForUserQuery here, claimQuery in dispatch.go, used by TryDispatch — which
// dispatch.go's own comment already documents as "no longer wired into main.go's live loop… but it
// remains a public claim path any future caller could invoke directly, so it honors
// dispatch_enabled too rather than relying on callers to remember the gate"). The suspend
// predicate goes in BOTH statements for the identical reason, as ONE shared constant referenced by
// both — not two hand-written copies, which would be the exact five-place-duplication hazard
// Phase 4 §8.1 already flagged applied to a sixth place. A test asserts both paths refuse (§12,
// T7b).
const notSuspendedFragment = `NOT EXISTS (SELECT 1 FROM send_byo.user_suspensions us WHERE us.user_id = j.user_id AND us.suspended = TRUE)`

// notPausedFragment is Phase 4 C1's per-CAMPAIGN pause (POST /v1/pause, send_byo_0059): a queued job whose
// campaign has a row in send_byo.campaign_pauses is not claimable. It is the sibling of notSuspendedFragment and
// follows the same rule for the same reason — "a filter is not a gate": the predicate is stated in EVERY statement
// that can claim a job (claimQuery in dispatch.go, claimForUserQuery below) and in the ring-membership snapshot
// (snapshotActiveUsers), as this ONE constant, never as hand-written copies. The check is therefore in the SAME
// statement (and so the same snapshot and the same transaction) that moves the job to `assigned`: there is no gap
// between "was this campaign paused?" and "claim it" in which a pause could be read stale.
//
// WHAT "SAME STATEMENT" DOES AND DOES NOT PROMISE. A claim statement that began before a pause COMMITTED reads the
// pre-pause snapshot and may still assign one job; that job is in flight, and an in-flight item finishes and is
// reported once (the contract). Every claim statement that begins after the pause commits sees the row and takes
// nothing. internal/dispatch/campaign_pause_test.go proves both halves (T13's shape, for pause).
//
// It is independent of notSuspendedFragment BY CONSTRUCTION: two tables, two predicates, ANDed. Lifting a
// suspension does not touch send_byo.campaign_pauses and resuming a campaign does not touch
// send_byo.user_suspensions, so neither can ever undo the other. The tenant is part of the match
// (cp.user_id = j.user_id) because the key of campaign_pauses is (campaign_id, user_id).
const notPausedFragment = `NOT EXISTS (SELECT 1 FROM send_byo.campaign_pauses cp WHERE cp.campaign_id = j.campaign_id AND cp.user_id = j.user_id)`

// claimForUserQuery is the LOCKED per-user claim (§1.1, CLAIM_FOR_USER), backed by the additive
// jobs_claim_by_user_idx (migration send_byo_0015). It keeps the Phase 1 draft 02 shape exactly —
// FOR UPDATE SKIP LOCKED, ORDER BY next_attempt_at, created_at, the same lease — generalized with
// a user_id predicate and a batch LIMIT. It sets ONLY state/lease/updated_at (no account, no worker)
// — worker/account are resolved per job afterward (§1.3), so a job can legally sit `assigned` with
// both assigned_account_id and assigned_box_id NULL between TXN 1 and TXN 2.
//
// notSuspendedFragment is included here too (§6.3) even though snapshotActiveUsers already
// excludes a suspended user from the ring in the live loop — this statement is a second,
// independent enforcement point so a bypass of the ring (a bug, or a future caller) still cannot
// claim on behalf of a suspended user.
//
// notPausedFragment (Phase 4 C1) is the same kind of second enforcement point for a PAUSED CAMPAIGN: the ring
// snapshot below also leaves such a job out, and this statement refuses it again at the moment of the claim.
const claimForUserQuery = `
WITH claimed AS (
    SELECT id FROM send_byo.jobs j
    WHERE j.state = 'queued' AND j.next_attempt_at <= NOW() AND j.user_id = $1
      AND j.cancel_requested_at IS NULL
      AND ` + notSuspendedFragment + `
      AND ` + notPausedFragment + `
    ORDER BY j.next_attempt_at ASC, j.created_at ASC
    FOR UPDATE SKIP LOCKED
    LIMIT $2
)
UPDATE send_byo.jobs
SET state = 'assigned',
    lease_expires_at = NOW() + make_interval(secs => $3),
    updated_at = NOW()
WHERE id IN (SELECT id FROM claimed)
RETURNING id, user_id, campaign_id, recipient_email, rendered_subject, rendered_body, rendered_text, unsubscribe_url, reply_to`

// ringSlot is one active user's turn this tick: how many lanes they may claim, and the RESOLVED
// per-user config this tick used to decide that grant. Module 3 Phase 6 §3.2: carrying uc alongside
// the slot (rather than re-resolving at Phase C+D) is what makes the three pacer sites agree WITHIN
// one tick — resolved once here in Phase B, threaded by parameter exactly like cfg, never re-read.
type ringSlot struct {
	userID string
	grant  int
	uc     userconfig.UserConfig
}

// Tick runs one full dispatch cycle (§1.2). It re-reads config fresh (Phase 2 OD-14 — a config
// change takes effect on the very next tick, no redeploy). Returns the Config it read (so the
// caller can time the next wait) and the number of jobs successfully pushed this tick.
func (d *Dispatcher) Tick(ctx context.Context) (Config, int, error) {
	cfg, err := LoadConfig(ctx, d.db)
	if err != nil {
		return Config{}, 0, err
	}

	// Phase 7 §4.1 step 1 (P7-5): STOP-CLAIM. Once SIGTERM has put us in the drain, this tick claims
	// nothing and pushes nothing — no queued→assigned transition, so no new job enters flight behind a
	// draining Control. Checked ONCE here at the top rather than per-job, so a tick already past this
	// point finishes the handoffs it had initiated ("lane workers finish only the handoffs already
	// initiated"). Nothing is written on this path: a draining tick is a pure no-op.
	if d.draining.Load() {
		return cfg, 0, nil
	}

	// Phase 9.5 B1 (§1.2) + fix-plan item 12: the dispatch kill-switch ladder. Checked at the very
	// top, before any claim/route/push — this tick performs NO claim, no route, no push, and writes
	// NOTHING. Queued jobs stay exactly queued (no state change, no job_events row); the outcome
	// handler, heartbeats, and config reads all continue independently (they are driven by their own
	// loops/handlers, not gated here). The loop keeps ticking at its normal interval — it does not
	// sleep-forever or exit — so it resumes the instant the rung is released. Throttled so a long
	// pause produces one log line per interval, never a flood.
	//
	// ClaimsAllowed is false at THREE rungs (accept_and_defer, refuse_new, stopped) and honours
	// dispatch_enabled=false as a severity floor, so this one gate covers the boolean's entire former
	// behaviour plus the two rungs above it. What differs between those rungs is enforced elsewhere:
	// intake at platformapi's handleRun, the reclaim sweep in ReclaimTick. See rung.go.
	if !cfg.ClaimsAllowed() {
		d.logDispatchPausedByOperator(cfg)
		return cfg, 0, nil
	}

	// Phase A: snapshot ground truth (two cheap reads; §1.2 A1/A2).
	inFlight, err := d.snapshotInFlight(ctx)
	if err != nil {
		return cfg, 0, err
	}
	activeUsers, err := d.snapshotActiveUsers(ctx)
	if err != nil {
		return cfg, 0, err
	}

	// Phase B: available lanes (the ceiling) + the pre-claim account-eligibility gate (§1.2/§6,
	// P4-5). lanes[U] = max(0, ceiling - in_flight[U]); a user already at ceiling is skipped (frees
	// no churn). A user with ZERO eligible accounts — including zero accounts at all — is likewise
	// skipped: their jobs stay queued, untouched, no claim/requeue churn, no job_events flooding
	// under sustained exhaustion (the rare-race requeue in resolveAndPush remains the only
	// backstop).
	// A per-user gate-check error must not abort the whole tick either (same no-starvation
	// reasoning as the claim loop below): log and skip that ONE user for this tick, leave every
	// other active user's gate evaluation unaffected.
	var ring []ringSlot
	for _, u := range activeUsers {
		// Module 3 Phase 6 §1/§3.2: resolve ONCE per user per tick, here — the ONLY resolution call
		// for this user this tick. The result is carried in the ringSlot and threaded to Admit
		// (below) and to claimForUser/Stamp in Phase C+D, never re-resolved at either site. Per
		// §1.3 property 4, a resolution error is an infrastructure fault (not "no override," which
		// userconfig.Resolve already treats as its own non-error default path) — same no-starvation
		// disposition as an evaluateChain failure: skip this one user, leave the ring unaffected.
		uc, ucErr := userconfig.Resolve(ctx, d.db, u, userconfig.Globals{
			SendDelayMinSec:    cfg.SendDelayMinSec,
			SendDelayMaxSec:    cfg.SendDelayMaxSec,
			ConcurrencyPerUser: cfg.ConcurrencyPerUserDefault,
			MaxCampaignSize:    cfg.MaxCampaignSize,
			MaxQueuedJobs:      cfg.MaxQueuedJobs,
			CampaignExpirySec:  cfg.CampaignExpirySec,
		})
		if ucErr != nil {
			log.Printf("dispatch: userconfig.Resolve(%s) failed: %v — skipping this user for this tick level=error", u, ucErr)
			continue
		}

		lanes := uc.ConcurrencyPerUser - inFlight[u]
		if lanes <= 0 {
			continue
		}
		// Phase 5 §5.1 wait-vs-fail: evaluate the user's resolved chain once. CLAIM (≥1 eligible)
		// adds them to the ring; WAIT (empty/unresolvable chain OR ≥1 self-recovering account) leaves
		// their jobs untouched in queued (no churn, exactly Phase 4's P4-5 gate); FAIL (a non-empty
		// chain whose every account is terminal_blocked) fails their ready jobs to terminal `failed`.
		verdict, err := d.evaluateChain(ctx, u, cfg)
		if err != nil {
			// Phase 7 §2.2 (P7-3): a TRANSPORT failure aborts the whole tick and surfaces to the
			// caller's outage gate, which pauses the loop. Skipping per-user is only correct for an
			// application error (that user's chain is unreadable); with the DB unreachable no other
			// user can be served either, so there is no starvation cost to aborting — and continuing
			// would hammer a dead socket once per active user, per tick.
			if IsOutage(err) {
				return cfg, 0, err
			}
			log.Printf("dispatch: evaluateChain(%s) failed: %v — skipping this user for this tick level=error", u, err)
			continue
		}
		switch verdict {
		case verdictClaim:
			// Phase 6 pacing (§1): withhold the portion of this user's grant whose lanes are still
			// cooling down. Admit returns min(lanes, paced-ready lanes) — a `min`, so it can only
			// FURTHER delay a send, never raise the grant above the resolved per-user ceiling
			// (`lanes`). A user with a positive lane count but no paced-ready slot this tick gets
			// grant 0: their jobs stay queued, untouched (claimed on a later tick once a timer
			// elapses) — the tick never blocks. Slots are stamped only on an ACTUAL push (Phase D).
			paced := d.pace.Admit(u, uc.ConcurrencyPerUser, lanes)
			if paced <= 0 {
				continue
			}
			ring = append(ring, ringSlot{userID: u, grant: paced, uc: uc})
		case verdictFail:
			if err := d.failExhaustedJobs(ctx, u); err != nil {
				log.Printf("dispatch: failExhaustedJobs(%s) failed: %v level=error", u, err)
			}
		default: // verdictWait — leave the user's jobs queued, untouched (no claim, no write, no churn)
		}
	}
	sort.Slice(ring, func(i, j int) bool { return ring[i].userID < ring[j].userID })

	// Phase C: rotate the ring to start at the first member after the fairness cursor (§5.1),
	// wrapping to the smallest if the cursor is past every member or unset/stale (a departed
	// campaign's id, e.g.). Every member is still visited this tick regardless of rotation —
	// no-starvation is structural (§5.2); the cursor only changes interleaving order across ticks.
	ring = rotateRing(ring, d.fairnessCursor())

	// Phase C+D: claim per active user (in rotated ring order), then resolve+push each claimed job.
	// A per-user claim error must NOT abort the whole tick — every OTHER ring member must still be
	// visited this tick (§5.2's no-starvation guarantee is "every ring member is visited every
	// tick"; a transient failure on one user's claim would otherwise starve everyone ordered after
	// them in the ring, unboundedly if the condition persists). Log and move to the next slot. The
	// fairness cursor advances AFTER the claim attempt (§1.2's own pseudocode: "batch =
	// CLAIM_FOR_USER(U, ...); rr_cursor = U"), not before — so a user is only recorded as
	// "serviced" once they were actually attempted.
	pushed := 0
	for _, slot := range ring {
		jobs, err := d.claimForUser(ctx, slot.userID, slot.grant, cfg.JobMaxProcessingTimeSec)
		d.setFairnessCursor(slot.userID)
		if err != nil {
			// Same §2.2 split as the gate check above: a transport failure aborts the tick (the loop
			// pauses; the claim transaction rolled back, so NOTHING was claimed — no job moved to
			// assigned, nothing to reconcile on resume). An application error still only skips this one
			// user, preserving §5.2's no-starvation reasoning.
			if IsOutage(err) {
				return cfg, pushed, err
			}
			log.Printf("dispatch: claimForUser(%s) failed: %v — skipping this user for this tick, ring continues level=error", slot.userID, err)
			continue
		}
		for _, job := range jobs {
			// Worker selection is orthogonal to routing (§1.3): workers are interchangeable, and Phase
			// 3's one-in-flight-per-worker constraint means a tick that claims more jobs than there
			// are idle workers must requeue the overflow cleanly rather than block.
			workerID := d.px.AnyConnectedWorker()
			if workerID == "" {
				d.requeue(ctx, job.id, "no_worker_available")
				continue
			}
			ok, perr := d.resolveAndPush(ctx, job, workerID, cfg)
			if perr != nil {
				continue // resolveAndPush already logs; the job was requeued or left claimable
			}
			if ok {
				// A send actually fired: cost this user one lane's cooldown (a fresh uniform delay
				// re-drawn per send, §1.1). Stamped ONLY on a real push, so a claim that returned
				// fewer jobs than granted, or a requeued/failed handoff, never burns a lane's timer.
				// Module 3 Phase 6 §3.2: uses slot.uc — the SAME resolved value Phase B's Admit call
				// used for this user this tick, never a fresh re-resolution — so Admit and Stamp can
				// never disagree on the ceiling within one tick, even if an admin PUT lands between
				// Phase B and this call (the exact race §3.2 exists to close).
				d.pace.Stamp(slot.userID, slot.uc.ConcurrencyPerUser, slot.uc.SendDelayMinSec, slot.uc.SendDelayMaxSec)
				pushed++
			}
		}
	}
	return cfg, pushed, nil
}

// (The P4-5 pre-claim gate's "has ≥1 eligible account?" check is now subsumed by evaluateChain
// (exhaustion.go), which extends it from claim/skip to claim/WAIT/FAIL — Phase 5 §5. The old
// standalone poolChainHasEligible was removed so there is exactly ONE chain-classification path and
// no risk of two predicates drifting.)

// fairnessCursor/setFairnessCursor guard Dispatcher.fairCursor (§5.1) under its own mutex —
// separate from rrMu (the account-cursor lock), since they protect independent state and are read
// from different call sites.
func (d *Dispatcher) fairnessCursor() string {
	d.fairMu.Lock()
	defer d.fairMu.Unlock()
	return d.fairCursor
}

func (d *Dispatcher) setFairnessCursor(userID string) {
	d.fairMu.Lock()
	d.fairCursor = userID
	d.fairMu.Unlock()
}

// rotateRing reorders ring (already sorted ascending by userID) to start at the first member
// with userID > cursor, wrapping to the beginning if none qualifies (cursor is unset, stale, or
// past every current member — §5.1's "resolves to the next id after the one we last served").
// Every element is kept; only the starting point changes.
func rotateRing(ring []ringSlot, cursor string) []ringSlot {
	if len(ring) == 0 {
		return ring
	}
	start := 0
	for i, slot := range ring {
		if slot.userID > cursor {
			start = i
			break
		}
		if i == len(ring)-1 {
			start = 0 // no member exceeds the cursor — wrap to the smallest
		}
	}
	return append(append([]ringSlot{}, ring[start:]...), ring[:start]...)
}

// snapshotInFlight is Phase A1 (§1.2): per-user count of jobs currently assigned/sending, served
// by the existing jobs_user_id_state_idx (user_id, state) — no new index needed.
func (d *Dispatcher) snapshotInFlight(ctx context.Context) (map[string]int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rows, err := d.db.Query(ctx,
		`SELECT user_id, COUNT(*) FROM send_byo.jobs WHERE state IN ('assigned','sending') GROUP BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var u string
		var n int
		if err := rows.Scan(&u, &n); err != nil {
			return nil, err
		}
		out[u] = n
	}
	return out, rows.Err()
}

// snapshotActiveUsers is Phase A2 (§1.2): the distinct set of users with claimable work right now,
// served by the existing jobs_claim_idx (next_attempt_at, created_at) WHERE state='queued'.
//
// Module 3 Phase 5 §6.2: a suspended user is excluded from THIS snapshot — not merely refused at
// the claim statement — which is what makes suspension genuinely lossless. If the gate sat only in
// the claim SQL, a suspended user whose accounts are ALL terminal_blocked would still reach
// evaluateChain, which would still return FAIL, and failExhaustedJobs would transition their jobs
// queued→failed: a suspension would DESTROY the user's queue, the exact opposite of "halt
// losslessly, nothing fails, the same rows resume." The NOT EXISTS against user_suspensions
// (served by the Phase 1 partial index, row-absent = not suspended = costs nothing for the common
// case) keeps a suspended user out of the ring entirely, so neither evaluateChain nor
// claimForUser ever runs for them.
//
// Phase 4 C1: a job of a PAUSED campaign (notPausedFragment) is left out the same way, and for the same reason. If
// the paused campaign were a ring member, a user whose accounts are all terminal_blocked would reach evaluateChain,
// get FAIL, and failExhaustedJobs would turn the paused campaign's queued items into `failed` — a pause would
// destroy the queue it was asked to hold. A user whose ONLY ready work is paused is simply not active this tick.
func (d *Dispatcher) snapshotActiveUsers(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rows, err := d.db.Query(ctx,
		`SELECT DISTINCT j.user_id FROM send_byo.jobs j
		  WHERE j.state='queued' AND j.next_attempt_at <= NOW()
		    AND j.cancel_requested_at IS NULL
		    AND NOT EXISTS (SELECT 1 FROM send_byo.user_suspensions us WHERE us.user_id = j.user_id AND us.suspended = TRUE)
		    AND `+notPausedFragment)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// claimForUser runs CLAIM_FOR_USER (§1.1) in one transaction with the actor GUC set, claiming up
// to `grant` queued jobs for userID. grant<=0 is a no-op (defensive; the caller already filters
// this, but a negative/zero grant must never claim unboundedly).
func (d *Dispatcher) claimForUser(ctx context.Context, userID string, grant, jobMaxProcessingTimeSec int) ([]claimedJob, error) {
	if grant <= 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := d.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('send_byo.actor', 'dispatcher', true)"); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, claimForUserQuery, userID, grant, jobMaxProcessingTimeSec)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []claimedJob
	for rows.Next() {
		var j claimedJob
		if err := rows.Scan(&j.id, &j.userID, &j.campaignID, &j.recipient, &j.subject, &j.body, &j.text, &j.unsubscribeURL, &j.replyTo); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return jobs, nil
}
