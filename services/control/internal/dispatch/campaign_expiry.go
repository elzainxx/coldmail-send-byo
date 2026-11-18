// campaign_expiry.go — Module 3 Phase 5 §5.3: the campaign expiry sweep, the FIFTH single-Control
// housekeeping loop's SECOND instance-in-fact (beside dispatch, reclaim, retention, and the
// account purge sweep — this is genuinely the sixth loop counting all of them, but §5.3 calls it
// "a fifth single-Control housekeeping loop" alongside the original four pre-Phase-3 loops; the
// account purge sweep landed in Phase 3 as its own fifth, so this is the loop count's second
// addition past the original four).
//
// THE ONE PLACE THIS PHASE DEPARTS FROM THE REPO'S "LAZY COMPARE AT POINT OF USE" IDIOM (§5.3, the
// spec's own words, worth repeating here): a lazy expiry check at the claim point would NEVER run
// on precisely the campaign that most needs bounding — a user whose every account is quarantined
// or throttled has jobs that are NEVER claimed (evaluateChain returns WAIT), so a claim-time check
// never executes. Expiry must fire IN THE ABSENCE of activity, which only a genuine periodic sweep
// can do.
//
// Suspension pauses this sweep's own clock (§7): the claim predicate is served by
// campaigns_expiry_sweep_idx, a PARTIAL index on `WHERE expired_at IS NULL AND clock_paused_at IS
// NULL` — a suspended campaign is not merely skipped by this sweep's query, it is not IN the index
// at all (Phase 1 §4.3's schema-level property). This file's own SELECT does not need to repeat
// the clock_paused_at predicate for correctness (the index already excludes those rows), but it is
// stated in the WHERE clause anyway so the query is correct even if the planner chose a different
// path — belt and suspenders on the one query that runs on every sweep tick.
package dispatch

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
)

// campaignExpiryTickTimeout bounds one whole sweep tick's DB work (same class of safety bound as
// the retention/purge sweeps' own tick timeouts).
const campaignExpiryTickTimeout = 60 * time.Second

// maxCampaignExpiryBatchPerTick bounds how many EXPIRED campaigns one tick processes before
// yielding to the next tick — a safety bound (not an operator dial), same shape as
// maxSweepBatchesPerTick/maxPurgeBatchesPerTick. Campaigns expiring in one tick is expected to be
// rare (one per genuinely-stuck user, not a bulk event), so this is generous.
const maxCampaignExpiryBatchPerTick = 256

// CampaignExpiryTick runs one sweep cycle (§5.3). It re-reads config fresh (config-not-code — a
// change to campaign_expiry_sweep_interval_ms takes effect on the very next tick, no redeploy),
// finds campaigns whose expires_at has passed (served by campaigns_expiry_sweep_idx), and for each:
// marks expired_at, fails that campaign's still-READY queued jobs to terminal 'failed' (mirroring
// failExhaustedJobs' shape — actor='dispatcher', last_error_class left as the job's own most recent
// real class, never overwritten with a made-up value, m2 §5.2 P5-14), and enqueues one
// notification. Returns the Config it read and the number of campaigns expired this tick.
func (d *Dispatcher) CampaignExpiryTick(ctx context.Context) (Config, int, error) {
	cfg, err := LoadConfig(ctx, d.db)
	if err != nil {
		return Config{}, 0, err
	}
	expired, err := d.sweepExpiredCampaigns(ctx, cfg)
	return cfg, expired, err
}

// sweepExpiredCampaigns finds and processes expired campaigns in bounded batches. Each campaign is
// processed in its OWN transaction (mirrors reclaim.go's per-job-tx rationale: bounds lock-hold
// time, and one campaign's failure to process must not abort the others in this tick).
func (d *Dispatcher) sweepExpiredCampaigns(ctx context.Context, cfg Config) (int, error) {
	// fix-plan item 13 / send_byo_0053: the shadow ladder. Read ONLY through the Effective* methods
	// (mutator_mode.go), which normalize an unrecognized value to shadow.
	//
	//   off      the candidate SELECT is not even issued. No count, no log line.
	//   shadow   identify the campaigns that WOULD be expired and log them with how many jobs would be
	//            failed; change nothing. THE SEEDED DEFAULT.
	//   enforce  the pre-0053 behaviour: expire each candidate below.
	//
	// WHY THIS SWEEP NEEDED A LADDER. Its job failures are TERMINAL — a job failed here is not re-queued
	// by correcting campaign_expiry_sec afterwards — and it fires on the ABSENCE of activity, which is
	// the hardest condition for an operator to predict in advance. An observation period is the only way
	// to find out that the window is set wrong before it costs a user their queued sends.
	if !cfg.CampaignExpiryDecides() {
		return 0, nil
	}

	ctx, cancel := context.WithTimeout(ctx, campaignExpiryTickTimeout)
	defer cancel()

	// A campaign that has NATURALLY COMPLETED (every one of its jobs already reached a terminal
	// state — sent/failed/bounced) is NOT expired, even past expires_at: expiry exists to bound a
	// STUCK campaign (§5.4's "jobs are never claimed" pathology), not to retroactively punish one
	// that simply finished slower than its window. The EXISTS check is what makes this
	// distinction — verified by TestCampaignExpiry_T5c's own control, which failed against the
	// naive "expires_at < NOW()" query alone before this term was added (found by running the
	// suite, not by inspection).
	rows, err := d.db.Query(ctx,
		`SELECT c.id, c.user_id FROM send_byo.campaigns c
		  WHERE c.expired_at IS NULL AND c.clock_paused_at IS NULL AND c.expires_at < NOW()
		    AND EXISTS (SELECT 1 FROM send_byo.jobs j WHERE j.campaign_id = c.id
		                  AND j.state IN ('queued','assigned','sending'))
		  ORDER BY c.expires_at LIMIT $1`, maxCampaignExpiryBatchPerTick)
	if err != nil {
		return 0, err
	}
	type expiredCampaign struct{ id, userID string }
	var candidates []expiredCampaign
	for rows.Next() {
		var c expiredCampaign
		if err := rows.Scan(&c.id, &c.userID); err != nil {
			rows.Close()
			return 0, err
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	// shadow: report the decision, change nothing. The candidate list above is the SAME query enforce
	// runs — including the EXISTS term that excludes a naturally-completed campaign — so the observation
	// is of the population enforce would actually act on, not of a wider one. An observation of the wrong
	// set would teach an operator to expect the wrong thing, which is worse than no observation.
	//
	// The JOB count is reported alongside the campaign count, because "2 campaigns" is not the number an
	// operator needs: the cost of promoting this dial is measured in TERMINALLY FAILED JOBS, and one
	// stuck campaign can hold thousands. Counted with the same predicate expireOneCampaign fails on
	// (state='queued' AND next_attempt_at <= NOW()), so the number is what would actually be failed
	// rather than the campaign's whole non-terminal population.
	if !cfg.CampaignExpiryExpires() {
		if len(candidates) == 0 {
			return 0, nil
		}
		ids := make([]string, 0, len(candidates))
		for _, c := range candidates {
			ids = append(ids, c.id)
		}
		var wouldFail int
		if err := d.db.QueryRow(ctx,
			`SELECT count(*) FROM send_byo.jobs
			  WHERE campaign_id = ANY($1::uuid[]) AND state='queued' AND next_attempt_at <= NOW()
			    AND cancel_requested_at IS NULL`,
			ids).Scan(&wouldFail); err != nil {
			return 0, err
		}
		log.Printf("campaign_expiry: campaign_expiry_shadow_would_expire — campaign_expiry_mode=%q, so NOTHING was expired this tick; %d campaign(s) past campaign_expiry_sec would be expired at campaign_expiry_mode=enforce, TERMINALLY failing %d ready queued job(s). Campaigns: %v (fix-plan item 13)",
			cfg.EffectiveCampaignExpiryMode(), len(candidates), wouldFail, ids)
		return 0, nil
	}

	total := 0
	for _, c := range candidates {
		if err := d.expireOneCampaign(ctx, c.id, c.userID); err != nil {
			log.Printf("campaign_expiry: campaign %s (user %s) failed to expire: %v level=error", c.id, c.userID, err)
			continue // a per-campaign error must not abort the rest of this tick's batch
		}
		total++
	}
	if len(candidates) >= maxCampaignExpiryBatchPerTick {
		log.Printf("campaign_expiry: per-tick batch cap reached (%d); remaining expired campaigns are swept on the next tick", maxCampaignExpiryBatchPerTick)
	}
	return total, nil
}

// expireOneCampaign applies §5.3's three effects for ONE campaign, atomically: mark expired_at,
// fail its ready queued jobs to terminal 'failed', and enqueue one notification. The UPDATE
// re-asserts `expired_at IS NULL AND clock_paused_at IS NULL` under the row's own lock so a
// campaign that was reinstated (clock un-paused mid-sweep) or already expired by a concurrent tick
// is a clean no-op (0 rows), not a double-fire. It ALSO re-asserts the "still has a non-terminal
// job" EXISTS check from sweepExpiredCampaigns' own SELECT — closing the window where a campaign's
// last job completes naturally between that SELECT and this per-campaign transaction, which would
// otherwise expire a campaign that finished a moment too late to be excluded by the outer query.
func (d *Dispatcher) expireOneCampaign(ctx context.Context, campaignID, userID string) error {
	return d.inActorTx(ctx, "dispatcher", func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE send_byo.campaigns SET expired_at = NOW(), updated_at = NOW()
			  WHERE id = $1 AND expired_at IS NULL AND clock_paused_at IS NULL AND expires_at < NOW()
			    AND EXISTS (SELECT 1 FROM send_byo.jobs j WHERE j.campaign_id = $1
			                  AND j.state IN ('queued','assigned','sending'))`,
			campaignID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil // raced (already expired, reinstated, or the campaign just completed) — clean no-op, not an error
		}

		// Fail this campaign's READY queued jobs (next_attempt_at <= NOW()) to terminal 'failed' —
		// the SAME shape as exhaustion.go's failExhaustedJobs, scoped by campaign_id instead of by
		// user_id (a user's OTHER campaigns, if the one-campaign-per-user invariant is ever relaxed
		// in a later phase, must not be touched by THIS campaign's expiry). last_error_class is left
		// as the job's own most recent real class — never overwritten with a made-up value (m2 §5.2
		// P5-14, the same discipline failExhaustedJobs already follows).
		if _, err := tx.Exec(ctx,
			`UPDATE send_byo.jobs
			   SET state='failed',
			       completed_at=NOW(),
			       last_error_message='campaign_expired: campaign_expiry_sec elapsed with no successful drain (§5.3)',
			       updated_at=NOW()
			 WHERE id IN (SELECT id FROM send_byo.jobs
			               WHERE campaign_id=$1 AND state='queued' AND next_attempt_at <= NOW()
			                 AND cancel_requested_at IS NULL
			               ORDER BY id FOR UPDATE)`,
			campaignID); err != nil {
			return err
		}

		// One notification (§5.3: "silent in advance, notifies on the event"). dedup_key scopes the
		// LIVE-row uniqueness to this campaign specifically (notifications_live_dedup_idx is
		// per-(user_id, dedup_key)), so a later DIFFERENT campaign's expiry for the same user is not
		// suppressed by this one's still-uncleared row.
		// copy-ledger: notif.campaign_expired
		if _, err := tx.Exec(ctx,
			`INSERT INTO send_byo.notifications (user_id, condition, subject_type, subject_id, severity, dedup_key, payload)
			 VALUES ($1, 'campaign_expired', 'campaign', $2, 'warning', $3, $4::jsonb)
			 ON CONFLICT (user_id, dedup_key) WHERE cleared_at IS NULL DO NOTHING`,
			userID, campaignID, "campaign_expired:"+campaignID, `{"campaign_id":"`+campaignID+`"}`); err != nil {
			return err
		}
		return nil
	})
}

// CampaignExpiryRunner drives the §5.3 housekeeping loop, on its own
// campaign_expiry_sweep_interval_ms. No Kick — a periodic sweep with no event to react to, the
// same shape as RetentionRunner/PurgeRunner: it exists specifically because a lazy claim-time check
// would never fire on the pathological case (every account terminal_blocked/throttled) this loop
// bounds.
type CampaignExpiryRunner struct {
	d *Dispatcher
}

func NewCampaignExpiryRunner(d *Dispatcher) *CampaignExpiryRunner { return &CampaignExpiryRunner{d: d} }

// Run loops until ctx is cancelled: sweep, then sleep for the freshly-read
// campaign_expiry_sweep_interval_ms. Sweeps once immediately at startup (a Control that was down past
// an expiry deadline has a backlog to process) — same reasoning as the other housekeeping loops.
func (r *CampaignExpiryRunner) Run(ctx context.Context) {
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

// tick runs one CampaignExpiryTick and returns the wait before the next. Same discipline as every
// other loop: recover a panic (Rule 7 — a sweep failure degrades to "skip this tick", never crashes
// the singleton Control), consult the SHARED outage gate, and honor the ADR-0011 leadership gate
// (this sweep writes send_byo.* rows, so only the lock-holding Control runs it).
func (r *CampaignExpiryRunner) tick(ctx context.Context) (interval time.Duration) {
	interval = defaultTickIntervalOnLoadErr
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("campaign_expiry: sweep PANIC recovered: %v — skipping this tick, will retry level=error", rec)
		}
	}()
	if ok, wait := r.d.health.Ready(ctx); !ok {
		return wait
	}
	if !r.d.IsLeader() {
		r.d.logNotLeader("campaign_expiry")
		return notLeaderRetryInterval
	}
	cfg, expired, err := r.d.CampaignExpiryTick(ctx)
	if err != nil {
		if r.d.health.Note(err) {
			return r.d.health.RetryIn()
		}
		log.Printf("campaign_expiry: sweep error: %v level=error", err)
		return defaultTickIntervalOnLoadErr
	}
	r.d.health.Note(nil)
	if expired > 0 {
		log.Printf("campaign_expiry: expired %d campaign(s) past campaign_expiry_sec with no successful drain", expired)
	}
	return time.Duration(cfg.CampaignExpirySweepIntervalMS) * time.Millisecond
}
