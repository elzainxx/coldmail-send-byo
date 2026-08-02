package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Real-infra integration test for the migration runner. Requires a real Postgres reachable
// via TEST_DATABASE_URL (an admin/owner-capable DSN) with public.users + public.job_items
// present. Skipped if TEST_DATABASE_URL is unset. NEVER mocks the DB (CLAUDE.md Rule 3).
func testDSN(t *testing.T) string {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping real-infra runner test")
	}
	return dsn
}

func mustExec(t *testing.T, c *pgx.Conn, sql string) {
	t.Helper()
	if _, err := c.Exec(context.Background(), sql); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func schemaMigrationsExists(t *testing.T, c *pgx.Conn) bool {
	t.Helper()
	var ok bool
	err := c.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema='byosmtp' AND table_name='schema_migrations')`).Scan(&ok)
	if err != nil {
		t.Fatalf("exists check: %v", err)
	}
	return ok
}

func versions(t *testing.T, c *pgx.Conn) []string {
	t.Helper()
	rows, err := c.Query(context.Background(), "SELECT version FROM byosmtp.schema_migrations ORDER BY version")
	if err != nil {
		t.Fatalf("select versions: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

// TestRunnerLifecycle proves apply → idempotent re-run → down on ONE connection, so there
// is no cross-process timing ambiguity: the exact same session observes each state change.
func TestRunnerLifecycle(t *testing.T) {
	dsn := testDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Admin connection used only to reset to a known-clean state and to observe truth.
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("admin connect: %v", err)
	}
	defer admin.Close(context.Background())

	// Clean slate on THIS connection. Drop only the SCHEMA (per-database); the cluster-global
	// roles byosmtp_owner/byosmtp_module are shared across databases (platform, template, this
	// one) and the bootstrap creates them idempotently (IF NOT EXISTS), so we do NOT drop
	// them here — dropping a role that still owns objects in another DB fails (2BP01). The
	// bootstrap's ownership/grant statements are idempotent against pre-existing roles.
	mustExec(t, admin, "DROP SCHEMA IF EXISTS byosmtp CASCADE")

	// ANTI-VACUITY: assert the precondition. If the schema already existed here, the test
	// below would be meaningless — so prove it's genuinely gone before we apply.
	if schemaMigrationsExists(t, admin) {
		t.Fatal("precondition failed: byosmtp.schema_migrations still exists after DROP — test would be vacuous")
	}

	r := &Runner{
		DSN:              dsn,
		Dir:              migrationsDirForTest(),
		PreflightPath:    preflightPathForTest(),
		LockTimeout:      "10s",
		StatementTimeout: "60s",
		AllowDown:        true,
	}

	// The full feature chain: bootstrap 0000, the locked Phase-1 data model sender_0001..0012,
	// then Phase 2's additive migrations sender_0013 (per-pool account uniqueness index) and
	// sender_0014 (the byosmtp.account_events audit sink), through module 2's close at 0024, then
	// module 3's own additions from 0025 (Docs/phase-1-schema-delta.md §6): 0025 new enum types +
	// event_actor ADD VALUE 'user', 0026 accounts' Phase-1 columns, 0027 accounts become
	// user_id-owned (drops pool_id), 0028 drops byosmtp.pools/pool_kind/default_shared_pool_id,
	// then G3's own additions 0029-0034: 0029 user_settings, 0030 user_config_overrides, 0031
	// user_suspensions, 0032 campaigns, 0033 notifications, 0034 widens
	// account_events_action_check; then Phase 3's own 0035 (credential_verification_state enum +
	// accounts columns), 0036 (the six account-surface config seeds), 0037 (M3-P3-OD-17/18: a
	// narrow INSERT-only grant on public.job_items so test-send can create its own job_item row),
	// and 0038 (M3-P3-OD-8/19: the bulk_add_max_rows seed C4's row-count bound needed); then
	// Phase 4's own 0039 (the seven From-gate DNS-evaluator config seeds), 0040 (the
	// acknowledgment columns + the eligibility predicate's third and final consolidated term +
	// the accounts_eligibility_idx WHERE-clause update, §8.1/§4.6), and 0041 (M3-P4-OD-16: widen
	// account_events_action_check for the operator break-glass override's own audit action,
	// discovered only while wiring the store-layer gate onto the adminapi surface); then Phase 5's
	// own 0042 (M3-P5-OD-1/§10.1: the ten dual-quarantine/un-quarantine/resource-protection config
	// seeds, PLUS the DELETE of the now-unconsumed quarantine_threshold row), 0043 (§1.3: the
	// job_events account_id+created_at index the failure-rate window query needs), and 0044
	// (M3-P5-OD-7: widen account_events_action_check for the per-user suspend/unsuspend audit
	// actions, discovered only while implementing C2's suspend feature — the fourth such migration
	// cascade in this build, after M3-P3-OD-18/19 and M3-P4-OD-16). This is the executable
	// expectation of the migration chain.
	wantChain := []string{"0000", "0001", "0002", "0003", "0004", "0005", "0006", "0007",
		"0008", "0009", "0010", "0011", "0012", "0013", "0014", "0015", "0016", "0017", "0018", "0019",
		"0020", "0021", "0022", "0023", "0024", "0025", "0026", "0027", "0028",
		"0029", "0030", "0031", "0032", "0033", "0034", "0035", "0036", "0037", "0038", "0039", "0040", "0041",
		"0042", "0043", "0044", "0045"}

	// --- Apply ---
	if err := r.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !schemaMigrationsExists(t, admin) {
		t.Fatal("after Up: byosmtp.schema_migrations does not exist")
	}
	if got := versions(t, admin); !equalStrings(got, wantChain) {
		t.Fatalf("after Up: versions = %v, want %v", got, wantChain)
	}
	// Prove the real objects/roles/grants exist (not just the tracking rows). The roles,
	// schema ownership, and platform grants are established by the bootstrap (0000).
	assertRole(t, admin, "byosmtp_owner", false /*login*/, false /*super*/)
	assertRole(t, admin, "byosmtp_module", true /*login*/, false /*super*/)
	assertSchemaOwner(t, admin, "byosmtp", "byosmtp_owner")
	assertPlatformGrant(t, admin, "users", "SELECT")
	// job_items gains INSERT at 0037 (M3-P3-OD-17) — checked again, post-0037, further below; this
	// EARLY assertion runs right after Up() reaches head, so it must already reflect 0037's grant
	// rather than the pre-0037 SELECT-only shape.
	assertPlatformGrant(t, admin, "job_items", "INSERT,SELECT")
	// Prove the Phase-1 feature objects exist and are owned by byosmtp_owner (so FORCE RLS on
	// byosmtp.accounts genuinely enforces — ADR-0004). "pools" removed at G2 — 0028 drops the
	// table, so by the time Up() reaches head it no longer exists to own-check. G3 adds the five
	// module-3 tables (Docs/phase-2-strip-shared-pool.md §5.1's ADR-0008 grants table).
	for _, tbl := range []string{"accounts", "boxes", "jobs", "job_events", "send_logs", "config", "account_events",
		"campaigns", "notifications", "user_config_overrides", "user_settings", "user_suspensions"} {
		assertTableOwner(t, admin, "byosmtp", tbl, "byosmtp_owner")
	}
	// Config seeds the 14 foundation keys (sender_0010) + Phase 5's 3 (sender_0017) + Phase 6's 2
	// (sender_0018: reclaim_tick_interval_ms, smtp_operation_timeout_sec) + Phase 7's 6 (sender_0019:
	// startup_reconnect_window_sec, shutdown_drain_timeout_sec, retention_sweep_interval_ms,
	// retention_delete_batch_size, rollup_granularity, box_outcome_buffer_ttl_sec) + Phase 8's 17
	// (sender_0020, appended to the SAME version file as Chunk 1's attempt_token column — flagged
	// migration-filename deviation, Docs/history/open-decisions.md P8-OD-1; amended 2026-07-26 from 14 to 17,
	// P8-OD-1 RULED-WIDEN) + Phase 9.5's 11 (sender_0021: B1-B5 operability controls + S10/S11
	// completeness dials) + G11-OD-1's 1 (sender_0023: brain_singleton_reacquire_interval_sec) = 54,
	// MINUS 1 at G2 (default_shared_pool_id deleted by 0028, M3-P2-OD-1) = 53, PLUS Phase 3's 6
	// (0036: account_test_rate_limit_per_day, account_count_cap_per_user,
	// account_retention_window_sec, bulk_add_verify_interval_ms, account_purge_sweep_interval_ms,
	// account_purge_delete_batch_size) = 59, PLUS Phase 3's 7th (0038: bulk_add_max_rows,
	// M3-P3-OD-8/19 — discovered only while implementing C4, after 0036's six had already
	// shipped) = 60, PLUS Phase 4's 7 (0039: from_gate_mode, from_dns_resolver,
	// from_dns_lookup_timeout_ms, from_dns_total_budget_ms, from_dns_cache_ttl_sec,
	// from_dns_negative_cache_ttl_sec, from_spf_lookup_limit_posture) = 67. 0040 adds NO config
	// key (schema/predicate only); Phase 7, Phase 9.5 and sender_0023 are fully schema-neutral
	// (config seeds only, no table); module 3's 0025-0027, 0029-0035 add no config key either.
	// 0042 (M3-P5-OD-1/§10.1) then SEEDS TEN and DELETES ONE (quarantine_threshold, its
	// consumer branch removed by this same phase's account_effect.go change): 67 - 1 + 10 = 76.
	// PLUS Phase 6's ONE (0045: test_button_rate_limit_per_user_per_day, M3-P3-OD-6's aggregate
	// test-button companion, landed by Phase 6's phase-start mechanical landing check rather than
	// by Phase 6's own new work) = 77.
	if n := configKeyCount(t, admin); n != 77 {
		t.Fatalf("after Up: byosmtp.config has %d keys, want 77 (76 pre-Phase-6 + 1 Phase 6 seed, M3-P3-OD-6)", n)
	}
	// Phase 8 Chunk 5's seventeen content-variation config seeds — anti-vacuity that they landed
	// (amended from fourteen, P8-OD-1 RULED-WIDEN: css_magnitude_px/zero_width_rate/user_agent_rate).
	for _, key := range []string{
		"variation_html_minify", "variation_comment_injection", "variation_comment_intensity",
		"variation_css_micro", "variation_css_magnitude_px", "variation_unique_url_params",
		"variation_zero_width", "variation_zero_width_rate",
		"variation_encoding_rotation", "variation_mime_boundary_rotation", "variation_dynamic_preheader",
		"variation_preheader_templates", "variation_header_style", "variation_user_agent_injection",
		"variation_user_agent_rate", "variation_precedence", "variation_thread_index",
	} {
		if !configKeyExists(t, admin, key) {
			t.Fatalf("after Up: byosmtp.config missing %q (sender_0020 Phase 8 seed not applied)", key)
		}
	}
	// Phase 4's ONE additive schema delta: the per-user claim index (sender_0015, CONCURRENTLY).
	// Proves the runner APPLIED a CONCURRENTLY migration (not a no-op) — anti-vacuity for the
	// non-transactional path fix.
	if !indexExists(t, admin, "jobs_claim_by_user_idx") {
		t.Fatal("after Up: byosmtp.jobs_claim_by_user_idx missing (sender_0015 CONCURRENTLY not applied)")
	}
	// Phase 5's additive columns (sender_0016). Anti-vacuity that the ADD COLUMN migration ran.
	if !columnExists(t, admin, "accounts", "consecutive_failures") {
		t.Fatal("after Up: byosmtp.accounts.consecutive_failures missing (sender_0016 not applied)")
	}
	if !columnExists(t, admin, "accounts", "throttle_paused_at") {
		t.Fatal("after Up: byosmtp.accounts.throttle_paused_at missing (sender_0016 not applied)")
	}
	// Phase 8 Chunk 1's additive column (sender_0020): the per-attempt identity that lets the Brain
	// reject a stale replay from a superseded attempt (P7-OD-6).
	if !columnExists(t, admin, "jobs", "attempt_token") {
		t.Fatal("after Up: byosmtp.jobs.attempt_token missing (sender_0020 not applied)")
	}
	// Phase 9.5's eleven operability + completeness config seeds (sender_0021) — anti-vacuity.
	for _, key := range []string{
		"dispatch_enabled", "autoquarantine_mode", "autopause_enabled", "variation_enabled",
		"alert_webhook_url", "health_metrics_window_sec", "brain_singleton_acquire_timeout_sec",
		"unit_price_credits", "variation_comment_intensity_rates", "variation_zero_width_rate_pct",
		"variation_css_micro_params",
	} {
		if !configKeyExists(t, admin, key) {
			t.Fatalf("after Up: byosmtp.config missing %q (sender_0021 Phase 9.5 seed not applied)", key)
		}
	}
	// sender_0022's shared error-transition predicate — the single definition both job_events
	// triggers gate their error context on. Anti-vacuity that the migration ran: it is a
	// CREATE OR REPLACE of two existing function BODIES, so no column/index/config check can see
	// it; the one NEW object it adds is this function.
	if !functionExists(t, admin, "fn_job_event_is_error_transition") {
		t.Fatal("after Up: byosmtp.fn_job_event_is_error_transition missing (sender_0022 not applied)")
	}
	// ...and that the replaced bodies actually GATE on it, which is the behavior 0022 exists for.
	// The predicate is IMMUTABLE SQL, so it can be evaluated directly: a success transition
	// (sending -> sent) is not an error, a requeue (assigned -> queued) is. Checking the predicate
	// rather than driving a job keeps this a migration test; the end-to-end "a SENT row carries no
	// error_class" assertion lives in the behavioral harness.
	if isErrorTransition(t, admin, "sending", "sent") {
		t.Fatal("after Up: fn_job_event_is_error_transition('sending','sent') is TRUE — a success would be tagged as an error (sender_0022 predicate wrong)")
	}
	if !isErrorTransition(t, admin, "assigned", "queued") {
		t.Fatal("after Up: fn_job_event_is_error_transition('assigned','queued') is FALSE — a requeue would lose its error context (sender_0022 predicate wrong)")
	}
	// sender_0023 (G11-OD-1 / ADR-0011 option B): the mid-life singleton re-acquire cadence. Its ONLY
	// object is one byosmtp.config row, so existence alone would be a weak check — assert the seeded
	// value AND default_value, because Rule 1 requires every key to ship a documented default_value
	// and the whole point of the key is that the cadence is configured, not hardcoded. A seed that
	// landed with a NULL/absent default would satisfy configKeyExists and still violate Rule 1.
	if !configKeyExists(t, admin, "brain_singleton_reacquire_interval_sec") {
		t.Fatal("after Up: byosmtp.config missing \"brain_singleton_reacquire_interval_sec\" (sender_0023 seed not applied)")
	}
	if v, d := configValueAndDefault(t, admin, "brain_singleton_reacquire_interval_sec"); v != `{"v": 5}` || d != `{"v": 5}` {
		t.Fatalf("after Up: brain_singleton_reacquire_interval_sec value=%s default_value=%s, want both {\"v\": 5} (sender_0023 seeded the wrong shape — the reader in main.go unwraps {\"v\": ...} and a bare 5 would silently fall back to 0 = never re-acquire)", v, d)
	}
	// sender_0024 (G12-OD-1 / ADR-0012 option A): job_events' append-only trigger gains the narrow
	// per-nullable-FK-column carve-out, so a parent DELETE's own `SET <col> = NULL` maintenance update
	// is permitted and the documented hard delete works again. This migration replaces ONE function
	// BODY and adds no object, so it is invisible to every existence check above — it has to be DRIVEN.
	// PAIRED, per Rule 10: the permitted half alone would report "landed" for a tree with the trigger
	// DROPPED, which is a strictly worse bug (a mutable audit log) than the wedge 0024 fixes.
	if !jobEventsCarveoutActive(t, admin) {
		t.Fatal("after Up: an ON DELETE SET NULL-shaped UPDATE on byosmtp.job_events (account_id -> NULL) was REJECTED — sender_0024's carve-out did not land, so DELETE /v1/admin/accounts/{id} is still wedged for any account with job history (ADR-0012)")
	}
	if !jobEventsContentUpdateRejected(t, admin) {
		t.Fatal("after Up: a CONTENT update on byosmtp.job_events (actor -> 'admin') was PERMITTED — sender_0024's carve-out is too wide, or the append-only trigger is gone entirely; the audit log is mutable")
	}

	// --- Module 3's own migrations, 0025-0028 (Docs/phase-1-schema-delta.md §6) ---
	// 0025: six new enum types + event_actor ADD VALUE 'user'.
	for _, typ := range []string{"quarantine_reason", "from_verification_state", "dmarc_policy",
		"suspend_reason", "notification_severity", "notification_state"} {
		if !enumTypeExists(t, admin, typ) {
			t.Fatalf("after Up: byosmtp.%s enum type missing (0025 not applied)", typ)
		}
	}
	if !enumValueExists(t, admin, "event_actor", "user") {
		t.Fatal("after Up: byosmtp.event_actor is missing value 'user' (0025 ADD VALUE not applied)")
	}
	// 0026: the Phase 1 §3.2/§4.4/§4.7 additive columns on accounts.
	for _, col := range []string{"quarantine_reason", "quarantined_at", "unquarantine_count_day",
		"unquarantine_reset_day", "from_verification_state", "from_dmarc_policy", "from_spf_aligned",
		"from_dmarc_aligned", "from_checked_at", "from_last_attempt_at", "from_last_attempt_error",
		"from_check_detail", "deleted_at", "deleted_by", "purge_after"} {
		if !columnExists(t, admin, "accounts", col) {
			t.Fatalf("after Up: byosmtp.accounts.%s missing (0026 not applied)", col)
		}
	}
	// 0027: pool_id gone, accounts_user_fk is RESTRICT (M3-P1-OD-12), the new indexes exist.
	if columnExists(t, admin, "accounts", "pool_id") {
		t.Fatal("after Up: byosmtp.accounts.pool_id still present (0027 did not drop it)")
	}
	if got := fkDeleteAction(t, admin, "accounts_user_fk"); got != "r" {
		t.Fatalf("after Up: accounts_user_fk ON DELETE action = %q, want %q (RESTRICT, M3-P1-OD-12 — the deliberately-restored tenant-erasure guard)", got, "r")
	}
	if !indexExists(t, admin, "accounts_user_host_username_unique") {
		t.Fatal("after Up: byosmtp.accounts_user_host_username_unique missing (0027 not applied)")
	}
	if !indexExists(t, admin, "accounts_eligibility_idx") {
		t.Fatal("after Up: byosmtp.accounts_eligibility_idx missing (0027 not applied)")
	}
	// 0028: byosmtp.pools and pool_kind are gone; default_shared_pool_id's row is gone.
	if tableExists(t, admin, "pools") {
		t.Fatal("after Up: byosmtp.pools still present (0028 did not drop it)")
	}
	if enumTypeExists(t, admin, "pool_kind") {
		t.Fatal("after Up: byosmtp.pool_kind still present (0028 did not drop it)")
	}
	if configKeyExists(t, admin, "default_shared_pool_id") {
		t.Fatal("after Up: byosmtp.config still has default_shared_pool_id (0028 did not delete the row, M3-P2-OD-1)")
	}

	// --- G3's own migrations, 0029-0034 (Docs/phase-2-strip-shared-pool.md §8.1 criterion 12) ---
	for _, tbl := range []string{"user_settings", "user_config_overrides", "user_suspensions", "campaigns", "notifications"} {
		if !tableExists(t, admin, tbl) {
			t.Fatalf("after Up: byosmtp.%s missing (G3 not applied)", tbl)
		}
	}
	if !columnExists(t, admin, "campaigns", "clock_paused_at") {
		t.Fatal("after Up: byosmtp.campaigns.clock_paused_at missing (0032 not applied)")
	}
	if !indexExists(t, admin, "campaigns_expiry_sweep_idx") {
		t.Fatal("after Up: byosmtp.campaigns_expiry_sweep_idx missing (0032 not applied)")
	}
	if !indexExists(t, admin, "notifications_live_dedup_idx") {
		t.Fatal("after Up: byosmtp.notifications_live_dedup_idx missing (0033 not applied)")
	}
	if !columnExists(t, admin, "user_settings", "routing_strategy") {
		t.Fatal("after Up: byosmtp.user_settings.routing_strategy missing (0029 not applied)")
	}
	if !columnExists(t, admin, "user_config_overrides", "send_delay_min_sec") {
		t.Fatal("after Up: byosmtp.user_config_overrides.send_delay_min_sec missing (0030 not applied)")
	}
	if !indexExists(t, admin, "user_suspensions_active_idx") {
		t.Fatal("after Up: byosmtp.user_suspensions_active_idx missing (0031 not applied)")
	}
	// 0034: the widened account_events_action_check accepts the three new module-3 actions.
	for _, action := range []string{"verified", "credential_updated", "purged"} {
		if !accountEventsActionAllowed(t, admin, action) {
			t.Fatalf("after Up: account_events_action_check rejects action %q (0034 did not widen it)", action)
		}
	}

	// --- Phase 3's own migrations, 0035-0036 (Docs/phase-3-user-owned-accounts.md §2.4/§7) ---
	if !enumTypeExists(t, admin, "credential_verification_state") {
		t.Fatal("after Up: byosmtp.credential_verification_state enum type missing (0035 not applied)")
	}
	for _, col := range []string{"credential_verification_state", "credential_verified_at",
		"credential_verify_at", "credential_verify_code", "credential_verify_response"} {
		if !columnExists(t, admin, "accounts", col) {
			t.Fatalf("after Up: byosmtp.accounts.%s missing (0035 not applied)", col)
		}
	}
	for _, key := range []string{"account_test_rate_limit_per_day", "account_count_cap_per_user",
		"account_retention_window_sec", "bulk_add_verify_interval_ms",
		"account_purge_sweep_interval_ms", "account_purge_delete_batch_size"} {
		if !configKeyExists(t, admin, key) {
			t.Fatalf("after Up: byosmtp.config missing %q (0036 not applied)", key)
		}
	}
	// 0037 (M3-P3-OD-17/18): the narrow INSERT-only grant on public.job_items for test-send.
	if !hasTablePrivilege(t, admin, "byosmtp_module", "public.job_items", "INSERT") {
		t.Fatal("after Up: byosmtp_module lacks INSERT on public.job_items (0037 not applied)")
	}

	// 0039 (Phase 4 §6.1): the seven From-gate config seeds.
	for _, key := range []string{"from_gate_mode", "from_dns_resolver", "from_dns_lookup_timeout_ms",
		"from_dns_total_budget_ms", "from_dns_cache_ttl_sec", "from_dns_negative_cache_ttl_sec",
		"from_spf_lookup_limit_posture"} {
		if !configKeyExists(t, admin, key) {
			t.Fatalf("after Up: byosmtp.config missing %q (0039 not applied)", key)
		}
	}
	// 0040 (Phase 4 §4.6): the two acknowledgment columns + the generated fingerprint column.
	for _, col := range []string{"from_warning_acknowledged_at", "from_warning_acknowledged_for", "from_verdict_fingerprint"} {
		if !columnExists(t, admin, "accounts", col) {
			t.Fatalf("after Up: byosmtp.accounts.%s missing (0040 not applied)", col)
		}
	}
	// 0041 (M3-P4-OD-16): the widened account_events_action_check accepts the operator
	// break-glass override's own audit action.
	if !accountEventsActionAllowed(t, admin, "from_gate_overridden") {
		t.Fatal("after Up: account_events_action_check rejects action \"from_gate_overridden\" (0041 not applied)")
	}
	// 0042 (Module 3 Phase 5 §10.1): the ten dual-quarantine/un-quarantine/resource-protection
	// config seeds, PLUS quarantine_threshold DELETED (M3-P5-OD-1).
	for _, key := range []string{"quarantine_consecutive_threshold", "quarantine_failure_rate_pct",
		"quarantine_failure_window_sec", "quarantine_min_sample", "unquarantine_max_per_day",
		"unquarantine_evidence_ttl_sec", "max_campaign_size", "max_queued_jobs",
		"campaign_expiry_sec", "campaign_expiry_sweep_interval_ms"} {
		if !configKeyExists(t, admin, key) {
			t.Fatalf("after Up: byosmtp.config missing %q (0042 not applied)", key)
		}
	}
	if configKeyExists(t, admin, "quarantine_threshold") {
		t.Fatal("after Up: byosmtp.config still has quarantine_threshold (0042 did not delete it, M3-P5-OD-1)")
	}
	// 0043 (§1.3): the job_events account_id+created_at index the failure-rate window needs.
	if !indexExists(t, admin, "job_events_account_id_created_at_idx") {
		t.Fatal("after Up: byosmtp.job_events_account_id_created_at_idx missing (0043 not applied)")
	}
	// 0044 (M3-P5-OD-7): the widened account_events_action_check accepts the suspend/unsuspend
	// audit actions.
	if !accountEventsActionAllowed(t, admin, "suspend") || !accountEventsActionAllowed(t, admin, "unsuspend") {
		t.Fatal("after Up: account_events_action_check rejects \"suspend\"/\"unsuspend\" (0044 not applied)")
	}
	// 0045 (Module 3 Phase 6, M3-P3-OD-6's aggregate test-button companion).
	if !configKeyExists(t, admin, "test_button_rate_limit_per_user_per_day") {
		t.Fatal("after Up: byosmtp.config missing \"test_button_rate_limit_per_user_per_day\" (0045 not applied)")
	}
	if v, d := configValueAndDefault(t, admin, "test_button_rate_limit_per_user_per_day"); v != `{"v": 20}` || d != `{"v": 20}` {
		t.Fatalf("after Up: test_button_rate_limit_per_user_per_day value=%s default_value=%s, want both {\"v\": 20} (0045 seeded the wrong shape)", v, d)
	}

	// FORCE RLS is set on the crown-jewel credential table.
	if !accountsForceRLS(t, admin) {
		t.Fatal("after Up: byosmtp.accounts does not have FORCE ROW LEVEL SECURITY")
	}

	// --- Idempotent re-run: applying again changes nothing, still exactly the full chain ---
	if err := r.Up(ctx); err != nil {
		t.Fatalf("Up (2nd): %v", err)
	}
	if got := versions(t, admin); !equalStrings(got, wantChain) {
		t.Fatalf("after idempotent Up: versions = %v, want %v", got, wantChain)
	}

	// --- Down: reverse the feature chain in strict reverse order (0015 -> 0001), proving each
	// migration's DOWN is real, then finally the bootstrap 0000 which drops the schema. The first
	// step (0015) exercises the runner's non-transactional DROP INDEX CONCURRENTLY down path. ---
	for i := len(wantChain) - 1; i >= 1; i-- {
		v := wantChain[i]
		if err := r.Down(ctx, v); err != nil {
			t.Fatalf("Down %s: %v", v, err)
		}
		if v == "0015" && indexExists(t, admin, "jobs_claim_by_user_idx") {
			t.Fatal("after Down 0015: jobs_claim_by_user_idx still present (CONCURRENTLY down did not drop it)")
		}
		if v == "0016" && (columnExists(t, admin, "accounts", "consecutive_failures") ||
			columnExists(t, admin, "accounts", "throttle_paused_at")) {
			t.Fatal("after Down 0016: a Phase 5 additive column is still present (down did not drop it)")
		}
		if v == "0018" && (configKeyExists(t, admin, "reclaim_tick_interval_ms") ||
			configKeyExists(t, admin, "smtp_operation_timeout_sec")) {
			t.Fatal("after Down 0018: a Phase 6 config seed is still present (down did not delete it)")
		}
		if v == "0019" && (configKeyExists(t, admin, "startup_reconnect_window_sec") ||
			configKeyExists(t, admin, "box_outcome_buffer_ttl_sec")) {
			t.Fatal("after Down 0019: a Phase 7 config seed is still present (down did not delete it)")
		}
		if v == "0020" && columnExists(t, admin, "jobs", "attempt_token") {
			t.Fatal("after Down 0020: byosmtp.jobs.attempt_token is still present (down did not drop it)")
		}
		if v == "0020" && configKeyExists(t, admin, "variation_html_minify") {
			t.Fatal("after Down 0020: a Phase 8 content-variation config seed is still present (down did not delete it)")
		}
		if v == "0021" && configKeyExists(t, admin, "dispatch_enabled") {
			t.Fatal("after Down 0021: a Phase 9.5 operability config seed is still present (down did not delete it)")
		}
		if v == "0022" && functionExists(t, admin, "fn_job_event_is_error_transition") {
			t.Fatal("after Down 0022: byosmtp.fn_job_event_is_error_transition is still present (down did not drop it)")
		}
		if v == "0023" && configKeyExists(t, admin, "brain_singleton_reacquire_interval_sec") {
			t.Fatal("after Down 0023: brain_singleton_reacquire_interval_sec is still present (down did not delete the seed)")
		}
		if v == "0037" && hasTablePrivilege(t, admin, "byosmtp_module", "public.job_items", "INSERT") {
			t.Fatal("after Down 0037: byosmtp_module still has INSERT on public.job_items (down did not revoke it)")
		}
		if v == "0038" && configKeyExists(t, admin, "bulk_add_max_rows") {
			t.Fatal("after Down 0038: byosmtp.config still has bulk_add_max_rows (down did not delete it)")
		}
		if v == "0044" && (accountEventsActionAllowed(t, admin, "suspend") || accountEventsActionAllowed(t, admin, "unsuspend")) {
			t.Fatal("after Down 0044: account_events_action_check still accepts \"suspend\"/\"unsuspend\" (down did not narrow it)")
		}
		if v == "0045" && configKeyExists(t, admin, "test_button_rate_limit_per_user_per_day") {
			t.Fatal("after Down 0045: byosmtp.config still has test_button_rate_limit_per_user_per_day (down did not delete it)")
		}
		if v == "0043" && indexExists(t, admin, "job_events_account_id_created_at_idx") {
			t.Fatal("after Down 0043: byosmtp.job_events_account_id_created_at_idx still present (down did not drop it)")
		}
		if v == "0042" {
			for _, key := range []string{"quarantine_consecutive_threshold", "quarantine_failure_rate_pct",
				"quarantine_failure_window_sec", "quarantine_min_sample", "unquarantine_max_per_day",
				"unquarantine_evidence_ttl_sec", "max_campaign_size", "max_queued_jobs",
				"campaign_expiry_sec", "campaign_expiry_sweep_interval_ms"} {
				if configKeyExists(t, admin, key) {
					t.Fatalf("after Down 0042: byosmtp.config still has %q (down did not delete it)", key)
				}
			}
			// The down file re-inserts quarantine_threshold VERBATIM from its sender_0010 seed
			// (Rule 1: default_value never mutated) — proves the down is a real reverse, not a
			// one-way DELETE that would leave a later re-Up() re-seeding a key the chain never lost.
			if !configKeyExists(t, admin, "quarantine_threshold") {
				t.Fatal("after Down 0042: byosmtp.config is missing quarantine_threshold (down did not restore it)")
			}
		}
		if v == "0041" && accountEventsActionAllowed(t, admin, "from_gate_overridden") {
			t.Fatal("after Down 0041: account_events_action_check still accepts \"from_gate_overridden\" (down did not narrow it)")
		}
		if v == "0040" && (columnExists(t, admin, "accounts", "from_warning_acknowledged_at") ||
			columnExists(t, admin, "accounts", "from_warning_acknowledged_for") ||
			columnExists(t, admin, "accounts", "from_verdict_fingerprint")) {
			t.Fatal("after Down 0040: a Phase 4 acknowledgment column is still present (down did not drop it)")
		}
		if v == "0039" {
			for _, key := range []string{"from_gate_mode", "from_dns_resolver", "from_dns_lookup_timeout_ms",
				"from_dns_total_budget_ms", "from_dns_cache_ttl_sec", "from_dns_negative_cache_ttl_sec",
				"from_spf_lookup_limit_posture"} {
				if configKeyExists(t, admin, key) {
					t.Fatalf("after Down 0039: byosmtp.config still has %q (down did not delete it)", key)
				}
			}
		}
		if v == "0036" {
			for _, key := range []string{"account_test_rate_limit_per_day", "account_count_cap_per_user",
				"account_retention_window_sec", "bulk_add_verify_interval_ms",
				"account_purge_sweep_interval_ms", "account_purge_delete_batch_size"} {
				if configKeyExists(t, admin, key) {
					t.Fatalf("after Down 0036: byosmtp.config still has %q (down did not delete it)", key)
				}
			}
		}
		if v == "0035" {
			for _, col := range []string{"credential_verification_state", "credential_verified_at",
				"credential_verify_at", "credential_verify_code", "credential_verify_response"} {
				if columnExists(t, admin, "accounts", col) {
					t.Fatalf("after Down 0035: byosmtp.accounts.%s still present (down did not drop it)", col)
				}
			}
			if enumTypeExists(t, admin, "credential_verification_state") {
				t.Fatal("after Down 0035: byosmtp.credential_verification_state enum type still present (down did not drop it)")
			}
		}
		if v == "0034" && accountEventsActionAllowed(t, admin, "purged") {
			t.Fatal("after Down 0034: account_events_action_check still permits 'purged' — down did not restore the 0014 CHECK")
		}
		if v == "0033" && tableExists(t, admin, "notifications") {
			t.Fatal("after Down 0033: byosmtp.notifications still present (down did not drop it)")
		}
		if v == "0032" && tableExists(t, admin, "campaigns") {
			t.Fatal("after Down 0032: byosmtp.campaigns still present (down did not drop it)")
		}
		if v == "0031" && tableExists(t, admin, "user_suspensions") {
			t.Fatal("after Down 0031: byosmtp.user_suspensions still present (down did not drop it)")
		}
		if v == "0030" && tableExists(t, admin, "user_config_overrides") {
			t.Fatal("after Down 0030: byosmtp.user_config_overrides still present (down did not drop it)")
		}
		if v == "0029" && tableExists(t, admin, "user_settings") {
			t.Fatal("after Down 0029: byosmtp.user_settings still present (down did not drop it)")
		}
		if v == "0028" {
			if !tableExists(t, admin, "pools") {
				t.Fatal("after Down 0028: byosmtp.pools is missing (down did not re-create it)")
			}
			if !enumTypeExists(t, admin, "pool_kind") {
				t.Fatal("after Down 0028: byosmtp.pool_kind is missing (down did not re-create it)")
			}
			if !configKeyExists(t, admin, "default_shared_pool_id") {
				t.Fatal("after Down 0028: byosmtp.config is missing default_shared_pool_id (down did not re-insert the row)")
			}
		}
		if v == "0027" {
			if !columnExists(t, admin, "accounts", "pool_id") {
				t.Fatal("after Down 0027: byosmtp.accounts.pool_id is missing (down did not re-add it)")
			}
			if got := fkDeleteAction(t, admin, "accounts_user_fk"); got != "c" {
				t.Fatalf("after Down 0027: accounts_user_fk ON DELETE action = %q, want %q (CASCADE restored) — the down must reverse the FK action too, or a rollback silently leaves the RESTRICT guard in place (M3-P1-OD-12)", got, "c")
			}
		}
		if v == "0026" {
			for _, col := range []string{"quarantine_reason", "from_verification_state", "deleted_at"} {
				if columnExists(t, admin, "accounts", col) {
					t.Fatalf("after Down 0026: byosmtp.accounts.%s is still present (down did not drop it)", col)
				}
			}
		}
		// 0025's down is the documented ONE-WAY DOOR (M3-P1-OD-7): Postgres has no
		// ALTER TYPE ... DROP VALUE, so 'user' must SURVIVE in event_actor even after this down —
		// asserting a reversal here would be asserting something that cannot happen. The six new
		// types it CAN drop must actually be gone.
		if v == "0025" {
			if !enumValueExists(t, admin, "event_actor", "user") {
				t.Fatal("after Down 0025: byosmtp.event_actor is missing value 'user' — impossible (Postgres has no DROP VALUE) unless the type itself was dropped/recreated, which 0025's down must not do")
			}
			for _, typ := range []string{"quarantine_reason", "from_verification_state", "dmarc_policy",
				"suspend_reason", "notification_severity", "notification_state"} {
				if enumTypeExists(t, admin, typ) {
					t.Fatalf("after Down 0025: byosmtp.%s is still present (down did not drop it)", typ)
				}
			}
		}
		// 0024's down restores sender_0008's UNCONDITIONAL body, deliberately reinstating the wedge —
		// that is what "reversible" means here. Driving the probe is the only way to see it, and this
		// assertion is what proves the down is real rather than a version-row deletion.
		if v == "0024" && jobEventsCarveoutActive(t, admin) {
			t.Fatal("after Down 0024: an account_id -> NULL update on byosmtp.job_events is STILL permitted (down did not restore sender_0008's unconditional reject-trigger)")
		}
	}
	// After reversing 0001..0012 the schema is still present (owned by the bootstrap) with
	// only version 0000 recorded — proving the feature-chain downs un-record without dropping
	// the bootstrap's schema/roles.
	if got := versions(t, admin); len(got) != 1 || got[0] != "0000" {
		t.Fatalf("after feature-chain down: versions = %v, want [0000]", got)
	}
	// Finally reverse the bootstrap itself: schema gone.
	if err := r.Down(ctx, "0000"); err != nil {
		t.Fatalf("Down 0000: %v", err)
	}
	if schemaMigrationsExists(t, admin) {
		t.Fatal("after Down 0000: byosmtp schema still present")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func assertTableOwner(t *testing.T, c *pgx.Conn, schema, table, owner string) {
	t.Helper()
	var got string
	err := c.QueryRow(context.Background(),
		`SELECT tableowner FROM pg_tables WHERE schemaname=$1 AND tablename=$2`, schema, table).Scan(&got)
	if err != nil {
		t.Fatalf("owner lookup %s.%s: %v", schema, table, err)
	}
	if got != owner {
		t.Fatalf("table %s.%s owner = %s, want %s", schema, table, got, owner)
	}
}

func configKeyCount(t *testing.T, c *pgx.Conn) int {
	t.Helper()
	var n int
	if err := c.QueryRow(context.Background(), "SELECT count(*) FROM byosmtp.config").Scan(&n); err != nil {
		t.Fatalf("config count: %v", err)
	}
	return n
}

// columnExists reports whether byosmtp.<table> has a column of the given name.
func columnExists(t *testing.T, c *pgx.Conn, table, column string) bool {
	t.Helper()
	var ok bool
	if err := c.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM information_schema.columns
		   WHERE table_schema='byosmtp' AND table_name=$1 AND column_name=$2)`, table, column).Scan(&ok); err != nil {
		t.Fatalf("column exists lookup %q.%q: %v", table, column, err)
	}
	return ok
}

// configKeyExists reports whether a byosmtp.config row with the given key is present (for the
// 0018-down anti-vacuity check: a Phase 6 seed must be gone after its down migration).
func configKeyExists(t *testing.T, c *pgx.Conn, key string) bool {
	t.Helper()
	var ok bool
	if err := c.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM byosmtp.config WHERE key=$1)`, key).Scan(&ok); err != nil {
		t.Fatalf("config key exists lookup %q: %v", key, err)
	}
	return ok
}

// configValueAndDefault returns a config row's value and default_value as text. Used by the
// sender_0023 anti-vacuity check: that migration's only object is a config row, so proving it
// "landed" means proving the JSONB {"v": ...} wrapper and the Rule-1 default_value are both right —
// not merely that a row with the key exists.
func configValueAndDefault(t *testing.T, c *pgx.Conn, key string) (string, string) {
	t.Helper()
	var value, def string
	if err := c.QueryRow(context.Background(),
		`SELECT value::text, default_value::text FROM byosmtp.config WHERE key=$1`, key).Scan(&value, &def); err != nil {
		t.Fatalf("config value lookup %q: %v", key, err)
	}
	return value, def
}

// indexExists reports whether an index of the given name exists in the byosmtp schema.
func indexExists(t *testing.T, c *pgx.Conn, name string) bool {
	t.Helper()
	var ok bool
	if err := c.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname='byosmtp' AND indexname=$1)`, name).Scan(&ok); err != nil {
		t.Fatalf("index exists lookup %q: %v", name, err)
	}
	return ok
}

// tableExists reports whether a base table of the given name exists in the byosmtp schema.
func tableExists(t *testing.T, c *pgx.Conn, name string) bool {
	t.Helper()
	var ok bool
	if err := c.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM pg_tables WHERE schemaname='byosmtp' AND tablename=$1)`, name).Scan(&ok); err != nil {
		t.Fatalf("table exists lookup %q: %v", name, err)
	}
	return ok
}

// enumTypeExists reports whether an enum (or any) type of the given name exists in the byosmtp
// schema (module 3's 0025/0028 anti-vacuity — Docs/phase-1-schema-delta.md §6).
// hasTablePrivilege reports whether role holds the given privilege on qualifiedTable (e.g.
// "public.job_items") — the anti-vacuity check for 0037's narrow INSERT-only grant (M3-P3-OD-17).
func hasTablePrivilege(t *testing.T, c *pgx.Conn, role, qualifiedTable, privilege string) bool {
	t.Helper()
	var ok bool
	if err := c.QueryRow(context.Background(),
		`SELECT has_table_privilege($1, $2, $3)`, role, qualifiedTable, privilege).Scan(&ok); err != nil {
		t.Fatalf("table privilege lookup %s on %s for %s: %v", privilege, qualifiedTable, role, err)
	}
	return ok
}

func enumTypeExists(t *testing.T, c *pgx.Conn, name string) bool {
	t.Helper()
	var ok bool
	if err := c.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM pg_type ty JOIN pg_namespace n ON n.oid=ty.typnamespace
		   WHERE n.nspname='byosmtp' AND ty.typname=$1)`, name).Scan(&ok); err != nil {
		t.Fatalf("type exists lookup %q: %v", name, err)
	}
	return ok
}

// enumValueExists reports whether enumType has the given value label (0025's ALTER TYPE
// event_actor ADD VALUE 'user' anti-vacuity, M3-P1-OD-7).
func enumValueExists(t *testing.T, c *pgx.Conn, enumType, value string) bool {
	t.Helper()
	var ok bool
	if err := c.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM pg_enum e
		   JOIN pg_type ty ON ty.oid = e.enumtypid
		   JOIN pg_namespace n ON n.oid = ty.typnamespace
		   WHERE n.nspname='byosmtp' AND ty.typname=$1 AND e.enumlabel=$2)`, enumType, value).Scan(&ok); err != nil {
		t.Fatalf("enum value exists lookup %s.%s: %v", enumType, value, err)
	}
	return ok
}

// accountEventsActionAllowed reports whether account_events_action_check's definition permits the
// given action literal — 0034's anti-vacuity (Docs/phase-2-strip-shared-pool.md §8.1 criterion 12):
// proving the CHECK's DDL was actually widened, not merely that the constraint still exists under
// the same name (a DROP+ADD with the OLD list would leave the name and object count unchanged).
func accountEventsActionAllowed(t *testing.T, c *pgx.Conn, action string) bool {
	t.Helper()
	var def string
	if err := c.QueryRow(context.Background(),
		`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'account_events_action_check'`).
		Scan(&def); err != nil {
		t.Fatalf("account_events_action_check definition lookup: %v", err)
	}
	return strings.Contains(def, "'"+action+"'")
}

// fkDeleteAction returns the confdeltype of the named foreign key constraint ('r'=RESTRICT,
// 'c'=CASCADE, 'n'=SET NULL, 'a'=NO ACTION) — 0027's M3-P1-OD-12 anti-vacuity: accounts_user_fk
// must be RESTRICT, not the inherited CASCADE.
func fkDeleteAction(t *testing.T, c *pgx.Conn, constraintName string) string {
	t.Helper()
	var action string
	// confdeltype is pg_catalog's "char" (single-byte internal type, OID 18) — pgx cannot scan it
	// into a Go string in binary format, so cast to text explicitly (this file's own ::text
	// pattern, e.g. configValueAndDefault).
	if err := c.QueryRow(context.Background(),
		`SELECT confdeltype::text FROM pg_constraint WHERE conname=$1`, constraintName).Scan(&action); err != nil {
		t.Fatalf("fk delete-action lookup %q: %v", constraintName, err)
	}
	return action
}

// functionExists reports whether a function of the given name exists in the byosmtp schema (for
// sender_0022, whose only NEW object is a function — its other two changes are CREATE OR REPLACE
// of bodies that already existed).
func functionExists(t *testing.T, c *pgx.Conn, name string) bool {
	t.Helper()
	var ok bool
	if err := c.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
		   WHERE n.nspname='byosmtp' AND p.proname=$1)`, name).Scan(&ok); err != nil {
		t.Fatalf("function exists lookup %q: %v", name, err)
	}
	return ok
}

// jobEventsCarveoutActive reports whether sender_0024's FK-aware carve-out is in force, by DRIVING
// the trigger rather than by reading its source. sender_0024 replaces one function BODY and adds no
// object at all, so functionExists/columnExists/configKeyExists are all blind to it and a
// pg_get_functiondef text match would assert the migration's own SQL against itself (it would pass
// against a carve-out that was syntactically present but logically wrong).
//
// So: insert a throwaway job_events row with account_id set, attempt the exact mutation the FK's
// ON DELETE SET NULL maintenance statement performs, and report whether it was permitted. Wrapped in
// a transaction that is always ROLLED BACK, so the lifecycle DB is unchanged either way. Runs as the
// admin/superuser connection deliberately — byosmtp_module has UPDATE revoked (sender_0008), so a
// rejection seen as that role would prove the REVOKE, not the trigger.
func jobEventsCarveoutActive(t *testing.T, c *pgx.Conn) bool {
	t.Helper()
	ctx := context.Background()
	tx, err := c.Begin(ctx)
	if err != nil {
		t.Fatalf("begin carve-out probe: %v", err)
	}
	defer tx.Rollback(ctx)

	// The FK chain job_events requires: a users row, an account, a job_item and a job. The probe
	// event row is INSERTed directly (not taken from the genesis trigger) because it needs
	// account_id already NON-NULL — the genesis row is born with it NULL, the job not yet being
	// assigned — and job_events rows cannot be updated into shape, which is the whole point.
	//
	// This probe runs BOTH after Up() reaches head (no pool_id column, 0027 dropped it) AND mid
	// down-reversal at the "0024" checkpoint (by which point 0027's own down has ALREADY restored
	// pool_id NOT NULL, since downs run in strict reverse order 0028,0027,0026,...). So the INSERT
	// shape must adapt to whichever schema is live RIGHT NOW rather than assume either one.
	var userID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO public.users (email, is_active) VALUES ('carveout-probe@sender.local', true) RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("carve-out probe: seed user: %v", err)
	}
	var acctID string
	if columnExists(t, c, "accounts", "pool_id") {
		var poolID string
		if err := tx.QueryRow(ctx,
			`INSERT INTO byosmtp.pools (user_id, kind, name) VALUES ($1,'shared','carveout-probe') RETURNING id`,
			userID).Scan(&poolID); err != nil {
			t.Fatalf("carve-out probe: seed pool: %v", err)
		}
		if err := tx.QueryRow(ctx,
			`INSERT INTO byosmtp.accounts
			   (user_id, pool_id, smtp_host, smtp_port, smtp_username,
			    credential_ciphertext, credential_key_id, credential_nonce, from_address)
			 VALUES ($1,$2,'probe.invalid',587,'probe@ourdomain.com','\x00','k','\x00','probe@ourdomain.com')
			 RETURNING id`, userID, poolID).Scan(&acctID); err != nil {
			t.Fatalf("carve-out probe: seed account (pool_id shape): %v", err)
		}
	} else {
		if err := tx.QueryRow(ctx,
			`INSERT INTO byosmtp.accounts
			   (user_id, smtp_host, smtp_port, smtp_username,
			    credential_ciphertext, credential_key_id, credential_nonce, from_address)
			 VALUES ($1,'probe.invalid',587,'probe@ourdomain.com','\x00','k','\x00','probe@ourdomain.com')
			 RETURNING id`, userID).Scan(&acctID); err != nil {
			t.Fatalf("carve-out probe: seed account (user-owned shape): %v", err)
		}
	}
	var jobItemID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO public.job_items (campaign_id, user_id, recipient_email, rendered_subject, rendered_body, status)
		 VALUES ($1,$1,'probe@example.com','S','<p>b</p>','pending') RETURNING id`, userID).Scan(&jobItemID); err != nil {
		t.Fatalf("carve-out probe: seed job_item: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('byosmtp.actor','system',true)`); err != nil {
		t.Fatalf("carve-out probe: set actor: %v", err)
	}
	var jobID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO byosmtp.jobs (job_item_id, campaign_id, user_id, recipient_email, rendered_subject, rendered_body, state, next_attempt_at)
		 VALUES ($1,$2,$2,'probe@example.com','S','<p>b</p>','queued',NOW()) RETURNING id`,
		jobItemID, userID).Scan(&jobID); err != nil {
		t.Fatalf("carve-out probe: seed job: %v", err)
	}
	var evID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO byosmtp.job_events (job_id, user_id, from_state, to_state, actor, account_id)
		 VALUES ($1,$2,'queued','assigned','dispatcher',$3) RETURNING id`,
		jobID, userID, acctID).Scan(&evID); err != nil {
		t.Fatalf("carve-out probe: seed job_events row: %v", err)
	}

	// THE PROBE: exactly what `ON DELETE SET NULL` issues. Permitted only under sender_0024.
	permitted := true
	if _, err := tx.Exec(ctx, `UPDATE byosmtp.job_events SET account_id=NULL WHERE id=$1`, evID); err != nil {
		permitted = false
		// A rejection must come from the append-only trigger; anything else means the probe itself is
		// broken and the answer would be meaningless.
		if !strings.Contains(err.Error(), "append-only: UPDATE forbidden") {
			t.Fatalf("carve-out probe: UPDATE failed for an unexpected reason (probe is broken, not the schema): %v", err)
		}
		// The transaction is now aborted; the deferred Rollback handles it.
	}
	return permitted
}

// jobEventsContentUpdateRejected is the paired NEGATIVE half of the probe above: with the carve-out
// active, a CONTENT mutation must STILL raise. Without this, jobEventsCarveoutActive alone would
// report "true" for a tree with the trigger dropped entirely — the exact regression that would turn
// an append-only audit log into a mutable one.
func jobEventsContentUpdateRejected(t *testing.T, c *pgx.Conn) bool {
	t.Helper()
	ctx := context.Background()
	tx, err := c.Begin(ctx)
	if err != nil {
		t.Fatalf("begin content-reject probe: %v", err)
	}
	defer tx.Rollback(ctx)

	var userID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO public.users (email, is_active) VALUES ('carveout-probe2@sender.local', true) RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("content-reject probe: seed user: %v", err)
	}
	var jobItemID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO public.job_items (campaign_id, user_id, recipient_email, rendered_subject, rendered_body, status)
		 VALUES ($1,$1,'probe2@example.com','S','<p>b</p>','pending') RETURNING id`, userID).Scan(&jobItemID); err != nil {
		t.Fatalf("content-reject probe: seed job_item: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('byosmtp.actor','system',true)`); err != nil {
		t.Fatalf("content-reject probe: set actor: %v", err)
	}
	var jobID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO byosmtp.jobs (job_item_id, campaign_id, user_id, recipient_email, rendered_subject, rendered_body, state, next_attempt_at)
		 VALUES ($1,$2,$2,'probe2@example.com','S','<p>b</p>','queued',NOW()) RETURNING id`,
		jobItemID, userID).Scan(&jobID); err != nil {
		t.Fatalf("content-reject probe: seed job: %v", err)
	}
	var evID string
	if err := tx.QueryRow(ctx,
		`SELECT id FROM byosmtp.job_events WHERE job_id=$1 ORDER BY seq LIMIT 1`, jobID).Scan(&evID); err != nil {
		t.Fatalf("content-reject probe: read genesis event: %v", err)
	}
	_, err = tx.Exec(ctx, `UPDATE byosmtp.job_events SET actor='admin' WHERE id=$1`, evID)
	return err != nil && strings.Contains(err.Error(), "append-only: UPDATE forbidden")
}

// isErrorTransition evaluates sender_0022's predicate directly. It is IMMUTABLE SQL over the
// job_state enum, so the migration's actual decision can be asserted without driving a job.
func isErrorTransition(t *testing.T, c *pgx.Conn, from, to string) bool {
	t.Helper()
	var ok bool
	if err := c.QueryRow(context.Background(),
		`SELECT byosmtp.fn_job_event_is_error_transition($1::byosmtp.job_state, $2::byosmtp.job_state)`,
		from, to).Scan(&ok); err != nil {
		t.Fatalf("is_error_transition(%q,%q): %v", from, to, err)
	}
	return ok
}

func accountsForceRLS(t *testing.T, c *pgx.Conn) bool {
	t.Helper()
	var relforcerowsecurity bool
	err := c.QueryRow(context.Background(),
		"SELECT relforcerowsecurity FROM pg_class WHERE oid='byosmtp.accounts'::regclass").Scan(&relforcerowsecurity)
	if err != nil {
		t.Fatalf("force rls lookup: %v", err)
	}
	return relforcerowsecurity
}

func assertRole(t *testing.T, c *pgx.Conn, name string, wantLogin, wantSuper bool) {
	t.Helper()
	var login, super bool
	err := c.QueryRow(context.Background(),
		"SELECT rolcanlogin, rolsuper FROM pg_roles WHERE rolname=$1", name).Scan(&login, &super)
	if err != nil {
		t.Fatalf("role %s not found: %v", name, err)
	}
	if login != wantLogin || super != wantSuper {
		t.Fatalf("role %s: login=%v super=%v, want login=%v super=%v", name, login, super, wantLogin, wantSuper)
	}
}

func assertSchemaOwner(t *testing.T, c *pgx.Conn, schema, owner string) {
	t.Helper()
	var got string
	err := c.QueryRow(context.Background(),
		"SELECT r.rolname FROM pg_namespace n JOIN pg_roles r ON n.nspowner=r.oid WHERE n.nspname=$1", schema).Scan(&got)
	if err != nil {
		t.Fatalf("schema %s owner lookup: %v", schema, err)
	}
	if got != owner {
		t.Fatalf("schema %s owner = %s, want %s", schema, got, owner)
	}
}

// assertPlatformGrant asserts byosmtp_module's exact privilege set on a public.* (platform-owned)
// table. want is comma-joined, alphabetically sorted to match information_schema's own
// string_agg ordering (e.g. "INSERT,SELECT") — callers must pass privileges in that order.
func assertPlatformGrant(t *testing.T, c *pgx.Conn, table, want string) {
	t.Helper()
	var priv string
	err := c.QueryRow(context.Background(),
		`SELECT string_agg(privilege_type, ',' ORDER BY privilege_type) FROM information_schema.role_table_grants
		 WHERE grantee='byosmtp_module' AND table_schema='public' AND table_name=$1`, table).Scan(&priv)
	if err != nil {
		t.Fatalf("grant lookup public.%s: %v", table, err)
	}
	if priv != want {
		t.Fatalf("public.%s grants to byosmtp_module = %q, want exactly %q", table, priv, want)
	}
}

func migrationsDirForTest() string {
	if v := os.Getenv("MIGRATIONS_DIR"); v != "" {
		return v
	}
	return "../../db/migrations"
}
func preflightPathForTest() string {
	if v := os.Getenv("PREFLIGHT_SQL"); v != "" {
		return v
	}
	return "../../db/preflight/check_platform_tables.sql"
}
