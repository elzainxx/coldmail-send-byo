// loop.go — the Phase-4 polling dispatch loop (§1.2): one tick snapshots ground truth, computes
// per-user lanes, applies the pre-claim pool-eligibility gate (§1.2/§6, P4-5), visits active
// campaigns in fairness-cursor order (§5), claims per active user (§1.1), and turns each claimed
// job into a push_job handoff. This is the PRODUCTION driver; TryDispatch (dispatch.go) is the
// Phase-3 single-shot primitive kept for its own behavioral tests.
package dispatch

import (
	"context"
	"log"
	"sort"
	"time"
)

// claimForUserQuery is the LOCKED per-user claim (§1.1, CLAIM_FOR_USER), backed by the additive
// jobs_claim_by_user_idx (migration sender_0015). It keeps the Phase 1 draft 02 shape exactly —
// FOR UPDATE SKIP LOCKED, ORDER BY next_attempt_at, created_at, the same lease — generalized with
// a user_id predicate and a batch LIMIT. It sets ONLY state/lease/updated_at (no account, no box)
// — box/account are resolved per job afterward (§1.3), so a job can legally sit `assigned` with
// both assigned_account_id and assigned_box_id NULL between TXN 1 and TXN 2.
const claimForUserQuery = `
WITH claimed AS (
    SELECT id FROM sender.jobs
    WHERE state = 'queued' AND next_attempt_at <= NOW() AND user_id = $1
    ORDER BY next_attempt_at ASC, created_at ASC
    FOR UPDATE SKIP LOCKED
    LIMIT $2
)
UPDATE sender.jobs
SET state = 'assigned',
    lease_expires_at = NOW() + make_interval(secs => $3),
    updated_at = NOW()
WHERE id IN (SELECT id FROM claimed)
RETURNING id, user_id, campaign_id, recipient_email, rendered_subject, rendered_body, reply_to`

// ringSlot is one active user's turn this tick: how many lanes they may claim.
type ringSlot struct {
	userID string
	grant  int
}

// Tick runs one full dispatch cycle (§1.2). It re-reads config fresh (Phase 2 OD-14 — a config
// change takes effect on the very next tick, no redeploy). Returns the Config it read (so the
// caller can time the next wait) and the number of jobs successfully pushed this tick.
func (d *Dispatcher) Tick(ctx context.Context) (Config, int, error) {
	cfg, err := LoadConfig(ctx, d.db)
	if err != nil {
		return Config{}, 0, err
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

	// Phase B: available lanes (the ceiling) + the pre-claim pool-eligibility gate (§1.2/§6, P4-5).
	// lanes[U] = max(0, ceiling - in_flight[U]); a user already at ceiling is skipped (frees no
	// churn). A user whose resolved pool chain has ZERO eligible accounts — including an empty
	// chain from an unset/invalid default_shared_pool_id, §4.3 — is likewise skipped: their jobs
	// stay queued, untouched, no claim/requeue churn, no job_events flooding under sustained
	// exhaustion (the rare-race requeue in resolveAndPush remains the only backstop).
	// A per-user gate-check error must not abort the whole tick either (same no-starvation
	// reasoning as the claim loop below): log and skip that ONE user for this tick, leave every
	// other active user's gate evaluation unaffected.
	var ring []ringSlot
	for _, u := range activeUsers {
		lanes := cfg.ConcurrencyPerUserDefault - inFlight[u]
		if lanes <= 0 {
			continue
		}
		hasEligible, err := d.poolChainHasEligible(ctx, u, cfg)
		if err != nil {
			log.Printf("dispatch: poolChainHasEligible(%s) failed: %v — skipping this user for this tick", u, err)
			continue
		}
		if !hasEligible {
			continue
		}
		ring = append(ring, ringSlot{userID: u, grant: lanes})
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
			log.Printf("dispatch: claimForUser(%s) failed: %v — skipping this user for this tick, ring continues", slot.userID, err)
			continue
		}
		for _, job := range jobs {
			// Box selection is orthogonal to routing (§1.3): boxes are interchangeable, and Phase
			// 3's one-in-flight-per-box constraint means a tick that claims more jobs than there
			// are idle boxes must requeue the overflow cleanly rather than block.
			boxID := d.px.AnyConnectedBox()
			if boxID == "" {
				d.requeue(ctx, job.id, "no_box_available")
				continue
			}
			ok, perr := d.resolveAndPush(ctx, job, boxID, cfg)
			if perr != nil {
				continue // resolveAndPush already logs; the job was requeued or left claimable
			}
			if ok {
				pushed++
			}
		}
	}
	return cfg, pushed, nil
}

// poolChainHasEligible is the P4-5 pre-claim gate: does userID's resolved pool chain (§4.2) have
// AT LEAST ONE pool with an eligible account right now? Reuses resolvePoolChain + poolHasEligible
// so the gate can never drift from the real selection logic (§4.1's own "defined identically"
// principle). An empty chain (no dedicated pool, no resolvable shared fallback) returns false —
// treated exactly like an exhausted chain, per §4.3.
func (d *Dispatcher) poolChainHasEligible(ctx context.Context, userID string, cfg Config) (bool, error) {
	pools, err := d.resolvePoolChain(ctx, userID, cfg)
	if err != nil {
		return false, err
	}
	for _, poolID := range pools {
		ok, err := d.poolHasEligible(ctx, poolID, cfg.DailyResetTimezone, cfg.ThrottlePauseSeconds)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

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
		`SELECT user_id, COUNT(*) FROM sender.jobs WHERE state IN ('assigned','sending') GROUP BY user_id`)
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
func (d *Dispatcher) snapshotActiveUsers(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rows, err := d.db.Query(ctx,
		`SELECT DISTINCT user_id FROM sender.jobs WHERE state='queued' AND next_attempt_at <= NOW()`)
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
	if _, err := tx.Exec(ctx, "SELECT set_config('sender.actor', 'dispatcher', true)"); err != nil {
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
		if err := rows.Scan(&j.id, &j.userID, &j.campaignID, &j.recipient, &j.subject, &j.body, &j.replyTo); err != nil {
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
