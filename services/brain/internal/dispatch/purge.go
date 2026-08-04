// purge.go — Module 3 Phase 3 §6.3: the account purge sweep. Hard-DELETEs a soft-deleted account
// row once its retention window has elapsed, removing credential_ciphertext/nonce/key_id together
// with no separate scrub step (Phase 1 §4.7 verified there is no second copy of the credential
// anywhere else in the schema — job_events/account_events/send_logs hold none).
//
// THIS IS THE HEAVIEST sender_0024/ADR-0012 CONSUMER IN EITHER MODULE (§6.3's own words). The
// purge selects accounts BECAUSE they are old, which is exactly the population most likely to have
// job_events history — so it hits the FK ON DELETE SET NULL path on essentially every row, where
// module 2's own ad-hoc admin delete hit it only occasionally. Without sender_0024's per-FK-column
// carve-out on fn_job_events_reject_update, the very first purge candidate with job history would
// abort with the append-only trigger's unconditional RAISE (module 2's own defect, ADR-0012). This
// package inherits 0024 in the migration chain (M3-P1-OD-2), so the purge WORKS rather than needing
// its own fix — but that is a fact to VERIFY (Rule 11), not assume, which is what T7/T10 do.
//
// SEPARATE from RetentionTick (retention.go) by ruling, M3-P3-OD-10: same cadence/gate shape argues
// for reuse, but a wedged purge is a PRIVACY failure (a user's deleted credential outliving its
// promised window) and a wedged send-log sweep is a STORAGE one — folding them together would let
// a healthy log sweep mask a broken purge. Fifth single-Brain loop, same discipline as the other
// four: fresh config each tick, the shared DBHealth outage gate, the ADR-0011 leadership gate,
// panic-recover, batch <= 0 disables the sweep, a per-tick batch cap that is LOGGED on hit rather
// than silently truncated.
package dispatch

import (
	"context"
	"log"
	"time"
)

// maxPurgeBatchesPerTick mirrors maxSweepBatchesPerTick's role for the send_logs sweep: a safety
// bound (not an operator dial) so a huge accumulated backlog cannot monopolize a connection or
// starve the dispatch/reclaim loops for DB throughput. With the seeded defaults that is
// 100 x 64 = 6400 accounts/tick — the account population this bounds is orders of magnitude
// smaller than send_logs, so the same constant is reused rather than a second one seeded for it.
const maxPurgeBatchesPerTick = 64

// purgeTickTimeout bounds one whole purge tick's DB work (same class of safety bound as the
// retention sweep's sweepTickTimeout).
const purgeTickTimeout = 60 * time.Second

// PurgeTick runs one purge sweep cycle (§6.3). It re-reads config fresh (config-not-code — a change
// to account_purge_delete_batch_size / account_purge_sweep_interval_ms takes effect on the very
// next tick, no redeploy), then DELETEs expired accounts in bounded batches until a batch comes
// back short (drained) or the per-tick batch cap is hit. Returns the Config it read (so the caller
// times the next wait) and the number of accounts purged this tick.
func (d *Dispatcher) PurgeTick(ctx context.Context) (Config, int, error) {
	cfg, err := LoadConfig(ctx, d.db)
	if err != nil {
		return Config{}, 0, err
	}
	purged, _, err := d.sweepExpiredAccounts(ctx, cfg)
	return cfg, purged, err
}

// sweepExpiredAccounts is the batched DELETE loop. batch <= 0 is treated as "no sweep" defensively
// (a mis-seeded zero must not become an unbounded DELETE); the seeded default is 100. Served by the
// Phase 1 accounts_purge_after_idx (partial on deleted_at IS NOT NULL).
//
// The claim-then-delete is ONE statement, not claim-then-delete-in-two-steps: a bounded sub-SELECT
// of ids feeding the DELETE, exactly retention.go's shape (Postgres has no LIMIT on DELETE). That
// makes the "claim" and the "delete" atomic by construction — there is no window between selecting
// a candidate and removing it in which a concurrent writer could observe or act on a half-claimed
// row, which is what §6.3's "the purge's claim-then-delete must be in one transaction" requires.
func (d *Dispatcher) sweepExpiredAccounts(ctx context.Context, cfg Config) (purged, batches int, err error) {
	batch := cfg.AccountPurgeDeleteBatchSize
	if batch <= 0 {
		log.Printf("purge: account_purge_delete_batch_size is %d — skipping sweep (a zero batch must never become an unbounded DELETE) level=warn", batch)
		return 0, 0, nil
	}
	ctx, cancel := context.WithTimeout(ctx, purgeTickTimeout)
	defer cancel()

	total := 0
	for i := 0; i < maxPurgeBatchesPerTick; i++ {
		// ONE bounded DELETE per batch. This is the sender_0024/ADR-0012 consumer: deleting an
		// account row with job_events history fires that table's two ON DELETE SET NULL FKs
		// (account_id, box_id), each an internal `UPDATE ONLY job_events SET <col> = NULL` that
		// the append-only trigger's per-FK-column carve-out must permit or this DELETE aborts.
		tag, err := d.db.Exec(ctx,
			`DELETE FROM byosmtp.accounts
			  WHERE id IN (
			        SELECT id FROM byosmtp.accounts
			         WHERE deleted_at IS NOT NULL AND purge_after < NOW()
			         ORDER BY purge_after
			         LIMIT $1)`, batch)
		if err != nil {
			return total, i, err
		}
		n := int(tag.RowsAffected())
		total += n
		if n < batch {
			return total, i + 1, nil // drained: fewer than a full batch remained
		}
	}
	// Cap hit: say so explicitly rather than letting a truncated sweep read as "fully drained"
	// (Rule 10's spirit applied to operations — the same anti-vacuity note retention.go carries).
	log.Printf("purge: per-tick batch cap reached (%d batches x %d rows = %d purged); remaining expired accounts are swept on the next tick",
		maxPurgeBatchesPerTick, batch, total)
	return total, maxPurgeBatchesPerTick, nil
}

// PurgeRunner drives the §6.3 housekeeping loop: the FIFTH single-Brain polling loop (beside
// dispatch, reclaim, retention and in-tick pacing), on its own account_purge_sweep_interval_ms.
// No Kick — a periodic sweep with no event to react to, same shape as RetentionRunner.
type PurgeRunner struct {
	d *Dispatcher
}

func NewPurgeRunner(d *Dispatcher) *PurgeRunner { return &PurgeRunner{d: d} }

// Run loops until ctx is cancelled: sweep, then sleep for the freshly-read
// account_purge_sweep_interval_ms. Sweeps once immediately at startup (a Brain that was down past a
// purge deadline has a backlog of expired accounts to remove) — same reasoning as RetentionRunner.
func (r *PurgeRunner) Run(ctx context.Context) {
	interval := r.tick(ctx)
	for {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			interval = r.tick(ctx)
		}
	}
}

// tick runs one PurgeTick and returns the wait before the next. Same discipline as the other four
// loops: recover a panic so a sweep failure degrades to "skip this tick" (Rule 7), and consult the
// SHARED Phase 7 §2 outage gate first so a DB outage pauses the sweep along with the rest.
func (r *PurgeRunner) tick(ctx context.Context) (interval time.Duration) {
	interval = defaultTickIntervalOnLoadErr
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("purge: sweep PANIC recovered: %v — skipping this tick, will retry level=error", rec)
		}
	}()
	if ok, wait := r.d.health.Ready(ctx); !ok {
		return wait
	}
	// ADR-0011 leadership gate (same as dispatch/reclaim/retention): the sweep DELETES rows, so
	// only the lock-holding Brain runs it (Rule 7: one owner of byosmtp.* DML).
	if !r.d.IsLeader() {
		r.d.logNotLeader("purge")
		return notLeaderRetryInterval
	}
	cfg, purged, err := r.d.PurgeTick(ctx)
	if err != nil {
		if r.d.health.Note(err) {
			return r.d.health.RetryIn()
		}
		log.Printf("purge: sweep error: %v level=error", err)
		return defaultTickIntervalOnLoadErr
	}
	r.d.health.Note(nil)
	if purged > 0 {
		log.Printf("purge: hard-deleted %d expired account(s) past their retention window (credential + record removed together)", purged)
	}
	return time.Duration(cfg.AccountPurgeSweepIntervalMS) * time.Millisecond
}
