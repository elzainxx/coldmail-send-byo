package adminapi

// health_test.go — Phase 9.5 Chunk 3 (B3, §3). Real Postgres, real HTTP against the actual
// production Server (httptest) — no mocks (CLAUDE.md Rule 3). Proves O4: with a KNOWN seeded mix
// (queued/sending/sent/failed/bounced jobs, a stale box, paused/quarantined accounts), every
// GET /v1/admin/health count matches a DIRECT DB COUNT — not merely "the endpoint returns 200" —
// and dispatch_paused mirrors the live dispatch_enabled config value.
//
// The pools[] headroom assertions this file carried through Phase 10 died at G2 (M3-P2-OD-12):
// queryPoolHeadroom is stubbed to always return an empty array once byosmtp.pools is dropped, and
// the real account-set-aware headroom replacement is G3 scope, alongside userRoutingStrategy's
// real user_settings read.

import (
	"context"
	"testing"
)

const sysUserHealth = "00000000-0000-0000-0000-000000000000"

// seedHealthUser ensures the system user exists (mirrors the dispatch package's own fixture
// convention — byosmtp.jobs/accounts/boxes all FK to public.users).
func (e *testEnv) seedHealthUser(t *testing.T) {
	t.Helper()
	if _, err := e.admin.Exec(bgCtx(),
		`INSERT INTO public.users (id, email, is_active) VALUES ($1,'sys@sender.local',true) ON CONFLICT (id) DO NOTHING`,
		sysUserHealth); err != nil {
		t.Fatalf("seed system user: %v", err)
	}
}

// seedHealthAccount seeds a real account owned by the system user with the given flags/counters
// and returns its id. credential_verification_state='verified' (+ credential_verified_at, the
// CHECK pairing): M3-P3-OD-4's eligibility term means the column's 'unverified' default is now
// STRUCTURALLY excluded from accountBaseEligibilityFilter, so this "normally healthy" fixture must
// seed 'verified' explicitly — the same shape every other "eligible by default" flag already has.
func (e *testEnv) seedHealthAccount(t *testing.T, paused, quarantined, disabled bool, dailySent, dailyLimit int) string {
	t.Helper()
	e.seedHealthUser(t)
	var id string
	if err := e.admin.QueryRow(bgCtx(),
		`INSERT INTO byosmtp.accounts
		   (user_id, smtp_host, smtp_port, smtp_username,
		    credential_ciphertext, credential_key_id, credential_nonce, from_address,
		    paused, pause_reason, quarantined, quarantine_reason, disabled, daily_sent_count, daily_limit, daily_reset_day,
		    credential_verification_state, credential_verified_at)
		 VALUES ($1,'smtp-sink',587,$2,'\x00','k','\x00','u@ourdomain.com',
		         $3, (CASE WHEN $3 THEN 'manual' ELSE NULL END)::byosmtp.pause_reason,
		         $4, (CASE WHEN $4 THEN 'manual' ELSE NULL END)::byosmtp.quarantine_reason,
		         $5, $6, $7, CURRENT_DATE, 'verified', NOW())
		 RETURNING id`,
		sysUserHealth, "u-"+randHex(6)+"@ourdomain.com", paused, quarantined, disabled, dailySent, dailyLimit).Scan(&id); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	return id
}

// seedHealthAccountDeleted seeds a healthy, full-headroom account and then soft-deletes it —
// M3-P4-OD-11's fixture: an account that would otherwise read as healthy, deleted.
func (e *testEnv) seedHealthAccountDeleted(t *testing.T) string {
	t.Helper()
	id := e.seedHealthAccount(t, false, false, false, 0, 100)
	if _, err := e.admin.Exec(bgCtx(),
		`UPDATE byosmtp.accounts SET deleted_at=NOW(), purge_after=NOW() + INTERVAL '30 days' WHERE id=$1`, id); err != nil {
		t.Fatalf("soft-delete account: %v", err)
	}
	return id
}

// seedHealthBox seeds a real box row with the given status and heartbeat age.
func (e *testEnv) seedHealthBox(t *testing.T, status string, heartbeatAgeSec int) string {
	t.Helper()
	e.seedHealthUser(t)
	var id string
	if err := e.admin.QueryRow(bgCtx(),
		`INSERT INTO byosmtp.boxes (id, user_id, token_hash, status, last_heartbeat_at, disabled_at)
		 VALUES (gen_random_uuid(), $1, 'x', $2::byosmtp.box_status, NOW() - INTERVAL '1 second' * $3,
		         CASE WHEN $2 = 'disabled' THEN NOW() ELSE NULL END)
		 RETURNING id`, sysUserHealth, status, heartbeatAgeSec).Scan(&id); err != nil {
		t.Fatalf("seed box: %v", err)
	}
	return id
}

// seedHealthJobItem + seedHealthJob seed a real job_item + a byosmtp.jobs row born in 'queued' via
// the genesis-event actor GUC, then (optionally) transitioned to a terminal/in-flight state with a
// real actor-GUC'd UPDATE — mirroring exactly how the dispatch package's own harness seeds jobs, so
// the job_events rows this test counts on are genuinely DB-triggered, not fabricated.
func (e *testEnv) seedHealthJob(t *testing.T, targetState string) string {
	t.Helper()
	e.seedHealthUser(t)
	ctx := bgCtx()
	var jobItemID string
	if err := e.admin.QueryRow(ctx,
		`INSERT INTO public.job_items (campaign_id, user_id, recipient_email, rendered_subject, rendered_body, reply_to, status)
		 VALUES ($1,$1,'h@example.com','Subj','<p>b</p>','reply@ourdomain.com','pending') RETURNING id`,
		sysUserHealth).Scan(&jobItemID); err != nil {
		t.Fatalf("seed job_item: %v", err)
	}
	tx, err := e.admin.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('byosmtp.actor','system',true)"); err != nil {
		t.Fatalf("set actor: %v", err)
	}
	var jobID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO byosmtp.jobs (job_item_id, campaign_id, user_id, recipient_email, rendered_subject, rendered_body, reply_to, state, next_attempt_at)
		 VALUES ($1,$2,$2,'h@example.com','Subj','<p>b</p>','reply@ourdomain.com','queued',NOW()) RETURNING id`,
		jobItemID, sysUserHealth).Scan(&jobID); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit job: %v", err)
	}
	if targetState == "queued" {
		return jobID
	}

	// Walk queued -> assigned -> sending -> targetState via real actor-GUC'd UPDATEs (mirrors the
	// dispatch harness's own T3-style state seeding), so job_events accumulates the real transition
	// chain rather than a single fabricated row.
	steps := []string{"assigned", "sending"}
	if targetState != "sending" {
		steps = append(steps, targetState)
	}
	for _, st := range steps {
		tx2, err := e.admin.Begin(ctx)
		if err != nil {
			t.Fatalf("begin step %s: %v", st, err)
		}
		if _, err := tx2.Exec(ctx, "SELECT set_config('byosmtp.actor','dispatcher',true)"); err != nil {
			t.Fatalf("set actor step %s: %v", st, err)
		}
		var setClause string
		switch st {
		case "assigned":
			setClause = `state='assigned', lease_expires_at=NOW()+INTERVAL '120 seconds'`
		case "sending":
			setClause = `state='sending'`
		case "sent":
			setClause = `state='sent', provider_message_id='PMID-'||id::text, completed_at=NOW(), lease_expires_at=NULL`
		case "failed":
			setClause = `state='failed', completed_at=NOW(), lease_expires_at=NULL`
		case "bounced":
			setClause = `state='bounced', completed_at=NOW(), lease_expires_at=NULL`
		}
		if _, err := tx2.Exec(ctx, `UPDATE byosmtp.jobs SET `+setClause+` WHERE id=$1`, jobID); err != nil {
			t.Fatalf("update step %s: %v", st, err)
		}
		if err := tx2.Commit(ctx); err != nil {
			t.Fatalf("commit step %s: %v", st, err)
		}
	}
	return jobID
}

// setHealthConfigBool/Str/Int write a config value directly (mirrors the dispatch harness's own
// setConfigBool/Str/Int, reimplemented here since adminapi's test harness has no equivalent yet).
func (e *testEnv) setHealthConfigBool(t *testing.T, key string, v bool) {
	t.Helper()
	if _, err := e.admin.Exec(bgCtx(),
		`UPDATE byosmtp.config SET value = jsonb_build_object('v', $2::boolean) WHERE key=$1`, key, v); err != nil {
		t.Fatalf("set config %s: %v", key, err)
	}
}
func (e *testEnv) setHealthConfigInt(t *testing.T, key string, v int) {
	t.Helper()
	if _, err := e.admin.Exec(bgCtx(),
		`UPDATE byosmtp.config SET value = jsonb_build_object('v', $2::int) WHERE key=$1`, key, v); err != nil {
		t.Fatalf("set config %s: %v", key, err)
	}
}

// TestHealth_ReportsTrueStateUnderKnownMix (O4) — seeds a known mix across jobs/boxes/accounts/
// pools and asserts EVERY /v1/admin/health count matches a direct DB COUNT, not just that the
// endpoint returns 200.
func TestHealth_ReportsTrueStateUnderKnownMix(t *testing.T) {
	e := newTestEnv(t, "health")
	ctx := context.Background()

	// --- Queue mix: one job in each terminal/in-flight state, plus extra queued. ---
	e.seedHealthJob(t, "queued")
	e.seedHealthJob(t, "queued")
	e.seedHealthJob(t, "sending")
	e.seedHealthJob(t, "sent")
	e.seedHealthJob(t, "failed")
	e.seedHealthJob(t, "bounced")

	// --- Boxes: one active (fresh heartbeat), one stale (old heartbeat), one disabled. ---
	e.seedHealthBox(t, "active", 5)
	e.seedHealthBox(t, "stale", 600)
	e.seedHealthBox(t, "disabled", 0)

	// --- Accounts: a mix of healthy/paused/quarantined/disabled/at-limit. ---
	e.seedHealthAccount(t, false, false, false, 0, 100)   // healthy
	e.seedHealthAccount(t, true, false, false, 0, 100)    // paused (manual)
	e.seedHealthAccount(t, false, true, false, 0, 100)    // quarantined
	e.seedHealthAccount(t, false, false, true, 0, 100)    // disabled
	e.seedHealthAccount(t, false, false, false, 100, 100) // at daily limit

	// --- dispatch_enabled: assert BOTH states mirror correctly. ---
	e.setHealthConfigBool(t, "dispatch_enabled", false)

	resp := e.do(t, "GET", "/v1/admin/health", nil).requireStatus(t, 200)

	// Queue counts vs direct DB truth.
	wantQueue := map[string]int{}
	rows, err := e.admin.Query(ctx, `SELECT state::text, COUNT(*) FROM byosmtp.jobs GROUP BY state`)
	if err != nil {
		t.Fatalf("direct queue count: %v", err)
	}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		wantQueue[st] = n
	}
	rows.Close()

	queue, ok := resp.body["queue"].(map[string]any)
	if !ok {
		t.Fatalf("response missing queue object: %s", resp.raw)
	}
	for _, st := range []string{"queued", "sending", "sent", "failed", "bounced"} {
		got := intField(t, queue, st)
		if got != wantQueue[st] {
			t.Fatalf("queue.%s=%d, want %d (direct DB COUNT) — full queue block: %+v", st, got, wantQueue[st], queue)
		}
	}

	// Boxes vs direct DB truth.
	var wantActive, wantStale, wantDisabled int
	if err := e.admin.QueryRow(ctx, `SELECT
		COUNT(*) FILTER (WHERE status='active'),
		COUNT(*) FILTER (WHERE status='stale'),
		COUNT(*) FILTER (WHERE status='disabled')
		FROM byosmtp.boxes`).Scan(&wantActive, &wantStale, &wantDisabled); err != nil {
		t.Fatalf("direct box count: %v", err)
	}
	boxes, ok := resp.body["boxes"].(map[string]any)
	if !ok {
		t.Fatalf("response missing boxes object: %s", resp.raw)
	}
	if got := intField(t, boxes, "active"); got != wantActive {
		t.Fatalf("boxes.active=%d, want %d", got, wantActive)
	}
	if got := intField(t, boxes, "stale"); got != wantStale {
		t.Fatalf("boxes.stale=%d, want %d", got, wantStale)
	}
	if got := intField(t, boxes, "disabled"); got != wantDisabled {
		t.Fatalf("boxes.disabled=%d, want %d", got, wantDisabled)
	}

	// Accounts vs direct DB truth.
	var wantPaused, wantQuarantined, wantDisabledAcc, wantAtLimit int
	if err := e.admin.QueryRow(ctx, `SELECT
		COUNT(*) FILTER (WHERE paused=TRUE),
		COUNT(*) FILTER (WHERE quarantined=TRUE),
		COUNT(*) FILTER (WHERE disabled=TRUE),
		COUNT(*) FILTER (WHERE daily_sent_count >= daily_limit)
		FROM byosmtp.accounts`).Scan(&wantPaused, &wantQuarantined, &wantDisabledAcc, &wantAtLimit); err != nil {
		t.Fatalf("direct account count: %v", err)
	}
	accounts, ok := resp.body["accounts"].(map[string]any)
	if !ok {
		t.Fatalf("response missing accounts object: %s", resp.raw)
	}
	if got := intField(t, accounts, "paused"); got != wantPaused {
		t.Fatalf("accounts.paused=%d, want %d", got, wantPaused)
	}
	if got := intField(t, accounts, "quarantined"); got != wantQuarantined {
		t.Fatalf("accounts.quarantined=%d, want %d", got, wantQuarantined)
	}
	if got := intField(t, accounts, "disabled"); got != wantDisabledAcc {
		t.Fatalf("accounts.disabled=%d, want %d", got, wantDisabledAcc)
	}
	if got := intField(t, accounts, "at_daily_limit"); got != wantAtLimit {
		t.Fatalf("accounts.at_daily_limit=%d, want %d", got, wantAtLimit)
	}

	// pools[] is a G2 stub (M3-P2-OD-12): always an empty array once byosmtp.pools is dropped. The
	// real account-set-aware headroom replacement is G3 scope.
	pools, ok := resp.body["pools"].([]any)
	if !ok {
		t.Fatalf("response missing pools array: %s", resp.raw)
	}
	if len(pools) != 0 {
		t.Fatalf("pools array = %+v, want empty (G2 stub)", pools)
	}

	// dispatch_paused mirrors dispatch_enabled=false.
	dispatch, ok := resp.body["dispatch"].(map[string]any)
	if !ok {
		t.Fatalf("response missing dispatch object: %s", resp.raw)
	}
	if paused, _ := dispatch["dispatch_paused"].(bool); !paused {
		t.Fatalf("dispatch.dispatch_paused=false with dispatch_enabled=false, want true: %+v", dispatch)
	}

	// Positive control: flip dispatch_enabled=true and confirm the field flips too — proves it
	// mirrors the LIVE config, not a cached/hardcoded value.
	e.setHealthConfigBool(t, "dispatch_enabled", true)
	resp2 := e.do(t, "GET", "/v1/admin/health", nil).requireStatus(t, 200)
	dispatch2, ok := resp2.body["dispatch"].(map[string]any)
	if !ok {
		t.Fatalf("response missing dispatch object (2nd): %s", resp2.raw)
	}
	if paused, _ := dispatch2["dispatch_paused"].(bool); paused {
		t.Fatalf("dispatch.dispatch_paused=true with dispatch_enabled=true, want false: %+v", dispatch2)
	}
}

// TestHealth_DeletedAccountNeverCountsHealthy (M3-P4-OD-11) — a soft-deleted account with full
// headroom and no other blocking flag must NOT count in accounts.healthy: an operator reading this
// endpoint must never see a real, send-capable-looking account that actually cannot route.
func TestHealth_DeletedAccountNeverCountsHealthy(t *testing.T) {
	e := newTestEnv(t, "healthdel")
	ctx := context.Background()

	healthy := e.seedHealthAccount(t, false, false, false, 0, 100)
	deleted := e.seedHealthAccountDeleted(t)

	resp := e.do(t, "GET", "/v1/admin/health", nil).requireStatus(t, 200)

	var wantHealthy int
	if err := e.admin.QueryRow(ctx,
		`SELECT COUNT(*) FROM byosmtp.accounts
		 WHERE paused=FALSE AND quarantined=FALSE AND disabled=FALSE
		   AND deleted_at IS NULL AND credential_verification_state = 'verified'
		   AND daily_sent_count < daily_limit`).Scan(&wantHealthy); err != nil {
		t.Fatalf("direct healthy count: %v", err)
	}
	if wantHealthy != 1 {
		t.Fatalf("fixture sanity: direct healthy count=%d, want exactly 1 (only %s, not the deleted %s)", wantHealthy, healthy, deleted)
	}

	accounts, ok := resp.body["accounts"].(map[string]any)
	if !ok {
		t.Fatalf("response missing accounts object: %s", resp.raw)
	}
	if got := intField(t, accounts, "healthy"); got != 1 {
		t.Fatalf("accounts.healthy=%d, want 1 — the deleted account %s must not be counted alongside the real healthy account %s", got, deleted, healthy)
	}

	// Negative control: undelete, and the count becomes 2 — proving the exclusion above is caused
	// by deleted_at specifically, not some other property of the fixture.
	if _, err := e.admin.Exec(ctx, `UPDATE byosmtp.accounts SET deleted_at=NULL, purge_after=NULL WHERE id=$1`, deleted); err != nil {
		t.Fatalf("undelete: %v", err)
	}
	resp2 := e.do(t, "GET", "/v1/admin/health", nil).requireStatus(t, 200)
	accounts2, ok := resp2.body["accounts"].(map[string]any)
	if !ok {
		t.Fatalf("response missing accounts object (2nd): %s", resp2.raw)
	}
	if got := intField(t, accounts2, "healthy"); got != 2 {
		t.Fatalf("negative control: accounts.healthy=%d after undelete, want 2 — if this doesn't rise to 2, the exclusion above proves nothing about deleted_at specifically", got)
	}
}

// TestHealth_WindowAffectsRateCounts — health_metrics_window_sec genuinely changes the reported
// rate, proving window_sec is read live from config, not hardcoded.
func TestHealth_WindowAffectsRateCounts(t *testing.T) {
	e := newTestEnv(t, "healthwindow")
	e.seedHealthJob(t, "sent")

	e.setHealthConfigInt(t, "health_metrics_window_sec", 3600)
	resp := e.do(t, "GET", "/v1/admin/health", nil).requireStatus(t, 200)
	if got := intField(t, resp.body, "window_sec"); got != 3600 {
		t.Fatalf("window_sec=%d, want 3600", got)
	}

	e.setHealthConfigInt(t, "health_metrics_window_sec", 60)
	resp2 := e.do(t, "GET", "/v1/admin/health", nil).requireStatus(t, 200)
	if got := intField(t, resp2.body, "window_sec"); got != 60 {
		t.Fatalf("window_sec=%d after config change, want 60 (no restart needed)", got)
	}
}

// TestHealth_RequiresAuth — the health endpoint is behind the SAME operator-key gate as every
// other /v1/admin/* route (Domain B) — proven with a missing bearer.
func TestHealth_RequiresAuth(t *testing.T) {
	e := newTestEnv(t, "healthauth")
	resp := e.doRaw(t, "GET", "/v1/admin/health", nil, "")
	resp.requireStatus(t, 401)
}

// intField extracts an int from a decoded JSON map, tolerating float64 (encoding/json's default
// numeric type) — a test helper, not a production path.
func intField(t *testing.T, m map[string]any, key string) int {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("field %q missing from %+v", key, m)
	}
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("field %q=%v is not a number", key, v)
	}
	return int(f)
}
