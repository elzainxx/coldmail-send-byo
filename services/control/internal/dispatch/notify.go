// notify.go — Phase 7 §3: the notification DELIVERY loop. Writing a send_byo.notifications row
// (EnqueueNotification, below) is cheap, synchronous with the event, and decoupled from delivery —
// the caller that discovers a failure never blocks on this loop. This file is the OTHER half:
// turning pending rows into delivered ones, on the loop's own ticker (notification_tick_interval_sec),
// never inside the dispatch tick and never poll-driven (§3.2 — module 2's fireWebhooks-on-health-poll
// shape is explicitly NOT reused here, since nobody polling means nobody notified).
//
// THREE MECHANISMS, ALL SCHEMA-LEVEL, NOT LOOP-CARE (§3.2/§3.3):
//   - Dedup is the partial unique index notifications_live_dedup_idx (user_id, dedup_key WHERE
//     cleared_at IS NULL) — EnqueueNotification's ON CONFLICT DO UPDATE advances last_seen_at on a
//     re-observation rather than inserting a duplicate row, so "one ongoing condition notifies once"
//     is a constraint, not a promise the Go code keeps by remembering to check first.
//   - Re-notify (a condition that has gone quiet long enough to be told again) is notified_at aged
//     past notification_dedup_window_sec — evaluated by this loop's own SELECT, not tracked
//     in-memory (§3.3's deliberate divergence from module 2's defaultAlertDedup map[string]bool).
//   - Digest batching is keyed by USER (§3.2's headline: ten accounts failing is ONE message,
//     because the group is the user, not the account and not the condition), windowed from the
//     OLDEST pending digest row for that user (first-event anchoring, not a fixed schedule).
package dispatch

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// notifyTickTimeout bounds one whole notification tick's DB work — same class of safety bound as
// the other housekeeping loops' tick timeouts.
const notifyTickTimeout = 60 * time.Second

// EnqueueNotification writes (or re-observes) one condition row (§3.2 step 1). Called SYNCHRONOUSLY
// with the event, inside the caller's own transaction (the account-effect transaction, the campaign
// expiry transaction, etc.) — never a separate connection, so the notification and the event that
// caused it commit together or not at all (no notification for an event that rolled back).
//
// dedupKey scopes notifications_live_dedup_idx's partial uniqueness: a LIVE (cleared_at IS NULL) row
// for (user_id, dedup_key) is a re-observation, not a new condition. ON CONFLICT DO UPDATE always
// advances last_seen_at (proving the condition was re-observed, §3.3's own test requirement) and
// refreshes payload (a later observation's payload — e.g. an updated subject list — is more current
// than the first). §3.3's re-notify rule is applied HERE, at the write site, not at delivery time:
// a row still 'pending' (never delivered) or already delivered/suppressed WITHIN the dedup window
// is left exactly as it is — one ongoing condition notifies once. A row delivered/suppressed OUTSIDE
// the window (notified_at older than dedupWindowSec) resets to 'pending' with a FRESH first_seen_at
// (so it re-enters digest batching exactly like a brand-new condition, rather than being treated as
// "overdue since the original first_seen_at" and skipping the batching window entirely) — this is
// the one write path that can move a row backward from delivered to pending, and it is conditioned
// on the SAME notified_at column the delivery loop stamps, so the two can never disagree about which
// window a row is in.
func EnqueueNotification(ctx context.Context, tx pgx.Tx, userID, condition, subjectType string, subjectID *string, severity string, dedupKey string, payload map[string]any, dedupWindowSec int) error {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO send_byo.notifications AS n (user_id, condition, subject_type, subject_id, severity, dedup_key, payload)
		 VALUES ($1, $2, $3, $4, $5::send_byo.notification_severity, $6, $7::jsonb)
		 ON CONFLICT (user_id, dedup_key) WHERE cleared_at IS NULL
		 DO UPDATE SET
		     last_seen_at = NOW(),
		     payload = EXCLUDED.payload,
		     state = CASE
		         WHEN n.state IN ('delivered', 'suppressed')
		              AND n.notified_at < NOW() - INTERVAL '1 second' * $8
		         THEN 'pending'::send_byo.notification_state
		         ELSE n.state
		     END,
		     notified_at = CASE
		         WHEN n.state IN ('delivered', 'suppressed')
		              AND n.notified_at < NOW() - INTERVAL '1 second' * $8
		         THEN NULL
		         ELSE n.notified_at
		     END,
		     first_seen_at = CASE
		         WHEN n.state IN ('delivered', 'suppressed')
		              AND n.notified_at < NOW() - INTERVAL '1 second' * $8
		         THEN NOW()
		         ELSE n.first_seen_at
		     END`,
		userID, condition, subjectType, subjectID, severity, dedupKey, payloadJSON, dedupWindowSec)
	return err
}

// ClearNotification marks a condition's LIVE row cleared (§3.3: "a cleared row does not block a
// fresh rising edge"). A no-op (0 rows) if there is no live row for this dedup_key — clearing an
// already-clear or never-fired condition is not an error.
//
// WHO ACTUALLY CALLS THIS, and why it is not this function (2026-08-03, defect #134). Every
// condition-RESOLVED path lives on an API surface, not in dispatch: this package raises quarantines
// and never lifts one, so it has no caller of its own and this copy is exercised only by
// notify_test.go. The four production resolution paths are platformapi's self-serve un-quarantine and
// credential-PATCH auto-clear, and adminapi's operator un-quarantine and credential-PATCH auto-clear
// — and M3-P8-OD-7 forbids either package importing internal/dispatch, so they CANNOT call this.
// Each owns a per-package duplicate (`clearAccountQuarantineNotification` in its own audit.go),
// which is the sanctioned idiom for that boundary. This comment previously read "Called when the
// caller observes the condition has resolved (e.g. self-serve un-quarantine, or the admin
// un-quarantine route)" — describing callers that had never been written and, across the OD-7
// boundary, never could be. That is what let the missing wiring look intentional for a whole phase:
// the notification stayed live after a resolve, so the NEXT quarantine of the same account folded
// into the stale row via notifications_live_dedup_idx and was suppressed for up to
// notification_dedup_window_sec (86400s default) — a genuinely new event, silently withheld for a day.
// If a future dispatch-side path ever does resolve a condition, call this; keep the three copies'
// dedup_key construction identical (each side asserts the coupling in its own test).
func ClearNotification(ctx context.Context, tx pgx.Tx, userID, dedupKey string) error {
	_, err := tx.Exec(ctx,
		`UPDATE send_byo.notifications SET cleared_at = NOW()
		  WHERE user_id = $1 AND dedup_key = $2 AND cleared_at IS NULL`,
		userID, dedupKey)
	return err
}

// NotificationTick runs one delivery cycle (§3.2). It re-reads config fresh (config-not-code — a
// change to any of the four notification dials takes effect on the very next tick, no redeploy),
// then: (1) delivers every eligible IMMEDIATE-severity pending row individually, (2) delivers, as
// ONE message per user, every user whose oldest pending DIGEST row has aged past
// notification_digest_window_sec. "Eligible" for both means notified_at IS NULL (never delivered)
// OR notified_at is older than notification_dedup_window_sec (§3.3's re-notify case) — a row
// delivered inside the dedup window is left pending, untouched, to be picked up once it ages out.
func (d *Dispatcher) NotificationTick(ctx context.Context) (Config, int, error) {
	cfg, err := LoadConfig(ctx, d.db)
	if err != nil {
		return Config{}, 0, err
	}
	delivered, err := deliverNotifications(ctx, d.db, cfg)
	return cfg, delivered, err
}

// deliverNotifications is NotificationTick's pure-DB-work half, split out so tests can drive it
// directly against a pool without going through the Dispatcher's config-load wrapper.
func deliverNotifications(ctx context.Context, db *pgxpool.Pool, cfg Config) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, notifyTickTimeout)
	defer cancel()

	immediate, err := deliverImmediate(ctx, db, cfg)
	if err != nil {
		return 0, err
	}
	digested, err := deliverDigests(ctx, db, cfg)
	if err != nil {
		return immediate, err
	}
	return immediate + digested, nil
}

// deliverImmediate delivers every PENDING immediate-severity row ONE AT A TIME (§3.2 step 2: "on
// the next notification tick, individually" — never folded into a user's digest, even if that user
// also has pending digest rows in the same tick). A 'pending' row is always eligible — §3.3's
// re-notify gate already ran at EnqueueNotification's write site, so this loop never re-derives it
// (the two could otherwise disagree about which window a row is in). "Eligible severity" is
// notification_immediate_severities, a config dial (§8), not a hardcoded tier check — an operator
// moving an event between tiers changes which rows this query selects, with no redeploy.
func deliverImmediate(ctx context.Context, db *pgxpool.Pool, cfg Config) (int, error) {
	if len(cfg.NotificationImmediateSeverities) == 0 {
		return 0, nil
	}
	rows, err := db.Query(ctx,
		`SELECT id FROM send_byo.notifications
		  WHERE state = 'pending' AND severity::text = ANY($1)
		  ORDER BY first_seen_at
		  LIMIT $2`,
		cfg.NotificationImmediateSeverities, maxNotifyBatchPerTick)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	delivered := 0
	for _, id := range ids {
		tag, err := db.Exec(ctx,
			`UPDATE send_byo.notifications SET state = 'delivered', notified_at = NOW()
			  WHERE id = $1 AND state = 'pending'`, id)
		if err != nil {
			log.Printf("notify: immediate delivery of %s failed: %v level=error", id, err)
			continue // one row's failure must not abort the rest of this tick's batch
		}
		delivered += int(tag.RowsAffected())
	}
	return delivered, nil
}

// maxNotifyBatchPerTick bounds how many rows one tick processes before yielding to the next tick —
// a safety bound (not an operator dial), same shape as the other loops' maxXBatchesPerTick.
const maxNotifyBatchPerTick = 500

// deliverDigests implements §3.2 step 3: for every user whose OLDEST eligible pending DIGEST row
// (i.e. excluding immediate-severity rows, which deliverImmediate already owns) has aged past
// notification_digest_window_sec, deliver ALL of that user's eligible pending digest rows as ONE
// message, grouped by condition. Suppressed (not silently dropped) rows are the OTHER rows in the
// same digest: state='suppressed' rather than 'delivered', so they remain traceable — the T2 exit
// criterion is exactly this, "not silently absent."
//
// "First-event anchoring" (§3.2): the window is measured from the OLDEST pending row's
// first_seen_at, not a fixed schedule — an event one second before a fixed boundary and its twin one
// second after would otherwise split into two messages, which is what dedup exists to prevent.
func deliverDigests(ctx context.Context, db *pgxpool.Pool, cfg Config) (int, error) {
	dueUsers, err := usersWithDueDigest(ctx, db, cfg)
	if err != nil {
		return 0, err
	}
	delivered := 0
	for _, userID := range dueUsers {
		n, err := deliverOneUserDigest(ctx, db, cfg, userID)
		if err != nil {
			log.Printf("notify: digest delivery for user %s failed: %v level=error", userID, err)
			continue // one user's failure must not abort the rest of this tick's batch
		}
		delivered += n
	}
	return delivered, nil
}

// usersWithDueDigest finds every user whose oldest PENDING non-immediate-severity row is older than
// notification_digest_window_sec. A 'pending' row is always eligible (§3.3's re-notify gate already
// ran at the write site), so this query only needs the digest window itself.
func usersWithDueDigest(ctx context.Context, db *pgxpool.Pool, cfg Config) ([]string, error) {
	rows, err := db.Query(ctx,
		`SELECT user_id
		   FROM send_byo.notifications
		  WHERE state = 'pending' AND NOT (severity::text = ANY($1))
		  GROUP BY user_id
		 HAVING MIN(first_seen_at) < NOW() - INTERVAL '1 second' * $2
		  LIMIT $3`,
		cfg.NotificationImmediateSeverities, cfg.NotificationDigestWindowSec, maxNotifyBatchPerTick)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// deliverOneUserDigest delivers ONE user's eligible pending digest rows as a single message: the
// FIRST (oldest) row transitions to 'delivered' (the message's own row, carrying every subject —
// §3.2's grouping), every OTHER eligible row for that user transitions to 'suppressed' (folded into
// the delivered row's message, not a second message, but not silently vanished either — its own
// state says so). Runs in ONE transaction so a mid-batch failure never leaves some rows delivered
// and others still pending from the same digest (which would silently split one message into two on
// the next tick's retry).
func deliverOneUserDigest(ctx context.Context, db *pgxpool.Pool, cfg Config, userID string) (int, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx,
		`SELECT id FROM send_byo.notifications
		  WHERE user_id = $1 AND state = 'pending' AND NOT (severity::text = ANY($2))
		  ORDER BY first_seen_at`,
		userID, cfg.NotificationImmediateSeverities)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil // raced with a concurrent tick (should not happen, single-Control, but a clean no-op either way)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE send_byo.notifications SET state = 'delivered', notified_at = NOW() WHERE id = $1`, ids[0]); err != nil {
		return 0, err
	}
	if len(ids) > 1 {
		if _, err := tx.Exec(ctx,
			`UPDATE send_byo.notifications SET state = 'suppressed', notified_at = NOW() WHERE id = ANY($1)`, ids[1:]); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// NotificationRunner drives the §3.2 delivery loop, on its own notification_tick_interval_sec — a
// dedicated ticker, never inside the dispatch tick (§3.2's own "not poll-driven" requirement).
type NotificationRunner struct {
	d *Dispatcher
}

func NewNotificationRunner(d *Dispatcher) *NotificationRunner { return &NotificationRunner{d: d} }

// Run loops until ctx is cancelled: deliver, then sleep for the freshly-read
// notification_tick_interval_sec. Delivers once immediately at startup (a Control that was down past
// a digest window has a backlog to flush) — same reasoning as every other housekeeping loop.
func (r *NotificationRunner) Run(ctx context.Context) {
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

// tick runs one NotificationTick and returns the wait before the next. Same discipline as every
// other loop: recover a panic (Rule 7 — a delivery failure degrades to "skip this tick", never
// crashes the singleton Control), consult the SHARED outage gate, and honor the ADR-0011 leadership
// gate (this loop writes send_byo.* rows, so only the lock-holding Control runs it).
//
// notifications_enabled is its OWN kill switch (§3.2), deliberately NOT coupled to dispatch_enabled
// — "halting dispatch is exactly when a user most needs to be told something." Checked AFTER the
// outage/leadership gates (those answer "can I write at all", this answers "should this specific
// loop act"), same ordering loop.go uses for dispatch_enabled.
func (r *NotificationRunner) tick(ctx context.Context) (interval time.Duration) {
	interval = defaultTickIntervalOnLoadErr
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("notify: delivery PANIC recovered: %v — skipping this tick, will retry level=error", rec)
		}
	}()
	if ok, wait := r.d.health.Ready(ctx); !ok {
		return wait
	}
	if !r.d.IsLeader() {
		r.d.logNotLeader("notify")
		return notLeaderRetryInterval
	}
	cfg, err := LoadConfig(ctx, r.d.db)
	if err != nil {
		if r.d.health.Note(err) {
			return r.d.health.RetryIn()
		}
		log.Printf("notify: config load error: %v level=error", err)
		return defaultTickIntervalOnLoadErr
	}
	r.d.health.Note(nil)
	if !cfg.NotificationsEnabled {
		return time.Duration(cfg.NotificationTickIntervalSec) * time.Second
	}
	delivered, err := deliverNotifications(ctx, r.d.db, cfg)
	if err != nil {
		log.Printf("notify: delivery error: %v level=error", err)
		return defaultTickIntervalOnLoadErr
	}
	if delivered > 0 {
		log.Printf("notify: delivered %d notification(s) this tick", delivered)
	}
	return time.Duration(cfg.NotificationTickIntervalSec) * time.Second
}
