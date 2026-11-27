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
		`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema='send_byo' AND table_name='schema_migrations')`).Scan(&ok)
	if err != nil {
		t.Fatalf("exists check: %v", err)
	}
	return ok
}

func versions(t *testing.T, c *pgx.Conn) []string {
	t.Helper()
	rows, err := c.Query(context.Background(), "SELECT version FROM send_byo.schema_migrations ORDER BY version")
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
	// roles send_byo_owner/send_byo_app are shared across databases (platform, template, this
	// one) and the bootstrap creates them idempotently (IF NOT EXISTS), so we do NOT drop
	// them here — dropping a role that still owns objects in another DB fails (2BP01). The
	// bootstrap's ownership/grant statements are idempotent against pre-existing roles.
	mustExec(t, admin, "DROP SCHEMA IF EXISTS send_byo CASCADE")

	// ANTI-VACUITY: assert the precondition. If the schema already existed here, the test
	// below would be meaningless — so prove it's genuinely gone before we apply.
	if schemaMigrationsExists(t, admin) {
		t.Fatal("precondition failed: send_byo.schema_migrations still exists after DROP — test would be vacuous")
	}

	r := &Runner{
		DSN:              dsn,
		Dir:              migrationsDirForTest(),
		PreflightPath:    preflightPathForTest(),
		LockTimeout:      "10s",
		StatementTimeout: "60s",
		AllowDown:        true,
	}

	// The full feature chain: bootstrap 0000, the locked Phase-1 data model send_byo_0001..0012,
	// then Phase 2's additive migrations send_byo_0013 (per-pool account uniqueness index) and
	// send_byo_0014 (the send_byo.account_events audit sink), through module 2's close at 0024, then
	// module 3's own additions from 0025 (Docs/history/phase-1-schema-delta.md §6): 0025 new enum types +
	// event_actor ADD VALUE 'user', 0026 accounts' Phase-1 columns, 0027 accounts become
	// user_id-owned (drops pool_id), 0028 drops send_byo.pools/pool_kind/default_shared_pool_id,
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
	// cascade in this build, after M3-P3-OD-18/19 and M3-P4-OD-16). Then 0050 (audit D5 /
	// alignment-fix-plan item 9: the (user_id, first_seen_at, id) index the new user-facing
	// notifications read route pages on — neither 0033 index can serve that query). Then 0054 (the P2
	// close-out's suspension READ route: the monotonic `version` column on user_suspensions that
	// GET /v1/admin/users/{id}/suspension returns). Then the Phase 3 close-out's 0055 (C4: the SMTP host
	// allowlist seed), 0056 (C1: the job_state enum value `cancelled`, a file of its own because Postgres
	// refuses to use a value in the transaction that added it) and 0057 (C1: the cancel_requested_at column,
	// the widened completed_at CHECK, the cancel guard trigger and the error-context predicate's new clause), and
	// 0058 (C5: the nullable jobs.rendered_text column, the optional plain-text part), and Phase 4's 0059 (C1: the
	// two NEW tables of POST /v1/pause and POST /v1/resume — send_byo.campaign_pauses, the state, and
	// send_byo.campaign_events, the append-only audit; no existing table is touched). Then Phase 5's 0060 (S4: the
	// nullable jobs.unsubscribe_url column and its CHECK, the source of the List-Unsubscribe and List-Unsubscribe-Post
	// headers) and 0061 (the owner's 30-day campaign_expiry_sec, re-seeded by DELETE + INSERT on 0048's precedent: no
	// new key, the row only). This is the executable expectation of the migration chain.
	wantChain := []string{"0000", "0001", "0002", "0003", "0004", "0005", "0006", "0007",
		"0008", "0009", "0010", "0011", "0012", "0013", "0014", "0015", "0016", "0017", "0018", "0019",
		"0020", "0021", "0022", "0023", "0024", "0025", "0026", "0027", "0028",
		"0029", "0030", "0031", "0032", "0033", "0034", "0035", "0036", "0037", "0038", "0039", "0040", "0041",
		"0042", "0043", "0044", "0045", "0046", "0047", "0048", "0049", "0050", "0051", "0052", "0053", "0054", "0055", "0056", "0057", "0058", "0059", "0060", "0061"}

	// --- Apply ---
	if err := r.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !schemaMigrationsExists(t, admin) {
		t.Fatal("after Up: send_byo.schema_migrations does not exist")
	}
	if got := versions(t, admin); !equalStrings(got, wantChain) {
		t.Fatalf("after Up: versions = %v, want %v", got, wantChain)
	}
	// Prove the real objects/roles/grants exist (not just the tracking rows). The roles,
	// schema ownership, and platform grants are established by the bootstrap (0000).
	assertRole(t, admin, "send_byo_owner", false /*login*/, false /*super*/)
	assertRole(t, admin, "send_byo_app", true /*login*/, false /*super*/)
	assertSchemaOwner(t, admin, "send_byo", "send_byo_owner")
	assertPlatformGrant(t, admin, "users", "SELECT")
	// job_items gains INSERT at 0037 (M3-P3-OD-17) — checked again, post-0037, further below; this
	// EARLY assertion runs right after Up() reaches head, so it must already reflect 0037's grant
	// rather than the pre-0037 SELECT-only shape.
	assertPlatformGrant(t, admin, "job_items", "INSERT,SELECT")
	// Prove the Phase-1 feature objects exist and are owned by send_byo_owner (so FORCE RLS on
	// send_byo.accounts genuinely enforces — ADR-0004). "pools" removed at G2 — 0028 drops the
	// table, so by the time Up() reaches head it no longer exists to own-check. G3 adds the five
	// module-3 tables (Docs/history/phase-2-strip-shared-pool.md §5.1's ADR-0008 grants table).
	for _, tbl := range []string{"accounts", "boxes", "jobs", "job_events", "send_logs", "config", "account_events",
		"campaigns", "notifications", "user_config_overrides", "user_settings", "user_suspensions"} {
		assertTableOwner(t, admin, "send_byo", tbl, "send_byo_owner")
	}
	// Config seeds the 14 foundation keys (send_byo_0010) + Phase 5's 3 (send_byo_0017) + Phase 6's 2
	// (send_byo_0018: reclaim_tick_interval_ms, smtp_operation_timeout_sec) + Phase 7's 6 (send_byo_0019:
	// startup_reconnect_window_sec, shutdown_drain_timeout_sec, retention_sweep_interval_ms,
	// retention_delete_batch_size, rollup_granularity, box_outcome_buffer_ttl_sec) + Phase 8's 17
	// (send_byo_0020, appended to the SAME version file as Chunk 1's attempt_token column — flagged
	// migration-filename deviation, Docs/history/open-decisions.md P8-OD-1; amended 2026-07-26 from 14 to 17,
	// P8-OD-1 RULED-WIDEN) + Phase 9.5's 11 (send_byo_0021: B1-B5 operability controls + S10/S11
	// completeness dials) + G11-OD-1's 1 (send_byo_0023: brain_singleton_reacquire_interval_sec) = 54,
	// MINUS 1 at G2 (default_shared_pool_id deleted by 0028, M3-P2-OD-1) = 53, PLUS Phase 3's 6
	// (0036: account_test_rate_limit_per_day, account_count_cap_per_user,
	// account_retention_window_sec, bulk_add_verify_interval_ms, account_purge_sweep_interval_ms,
	// account_purge_delete_batch_size) = 59, PLUS Phase 3's 7th (0038: bulk_add_max_rows,
	// M3-P3-OD-8/19 — discovered only while implementing C4, after 0036's six had already
	// shipped) = 60, PLUS Phase 4's 7 (0039: from_gate_mode, from_dns_resolver,
	// from_dns_lookup_timeout_ms, from_dns_total_budget_ms, from_dns_cache_ttl_sec,
	// from_dns_negative_cache_ttl_sec, from_spf_lookup_limit_posture) = 67. 0040 adds NO config
	// key (schema/predicate only); Phase 7, Phase 9.5 and send_byo_0023 are fully schema-neutral
	// (config seeds only, no table); module 3's 0025-0027, 0029-0035 add no config key either.
	// 0042 (M3-P5-OD-1/§10.1) then SEEDS TEN and DELETES ONE (quarantine_threshold, its
	// consumer branch removed by this same phase's account_effect.go change): 67 - 1 + 10 = 76.
	// PLUS Phase 6's ONE (0045: test_button_rate_limit_per_user_per_day, M3-P3-OD-6's aggregate
	// test-button companion, landed by Phase 6's phase-start mechanical landing check rather than
	// by Phase 6's own new work) = 77. PLUS Phase 7's TEN (0046: notification_digest_window_sec,
	// notification_dedup_window_sec, notification_immediate_severities, notifications_enabled,
	// notification_tick_interval_sec, destination_throttle_window_sec,
	// destination_throttle_user_threshold, quote_capacity_projection_enabled,
	// quote_long_duration_warn_sec, score_alert_threshold — the last REPLACING quarantine_threshold
	// under a new name per M3-P5-OD-1's split, already counted as a deletion at 0042 above, so this
	// is a net +10 not +9) = 87. MINUS Phase 8's TWO (0049: retry_count_4xx and
	// variation_zero_width_rate_pct, M3-P8-OD-8 — the first NET DECREASE in the chain that is not
	// offset by a same-migration re-seed; both were RESERVED with a Phase 8 expiry that came due
	// and neither ever had a genuine module-3 consumer, so ADR-0016's two-condition test permits
	// the row drop) = 85. MINUS ONE at 0051 (score_alert_threshold, ruled 2026-08-05 — the third key
	// that carried an expired RESERVED marker; 0049 kept it and re-pointed its expiry at the
	// open-ended future-work item FW-M3-12, and that retarget is what the ruling reverses, since an
	// expiry pointing at an item with no date can never come due. ADR-0016's two-condition test is
	// satisfied: it never had a module-3 consumer at any point — Phase 7's spec CLAIMED to be that
	// consumer and was not — and there is no replacement key to reinterpret) = 84. PLUS ONE at 0052
	// (dispatch_mode, fix-plan item 12 — the graduated dispatch kill-switch ladder; the first key added
	// outside a numbered phase, and notably NOT paired with a drop of dispatch_enabled: ADR-0017 records
	// why ADR-0016's two-condition test FAILS condition (1) there, the boolean having many live
	// consumers, so it is composed as a severity floor instead of removed) = 85. PLUS FOUR at 0053
	// (autopause_mode, retention_mode, campaign_expiry_mode, account_purge_mode — fix-plan item 13,
	// ADR-0018: a shadow rung for every autonomous mutator that did not already have one. FOUR here
	// against module 2's TWO, because this module runs two autonomous sweeps it does not — campaign
	// expiry and the account purge — and those are the two most irreversible writers in the module:
	// campaign expiry fails jobs TERMINALLY, and the purge hard-DELETEs a credential. The dial count is
	// a property of THIS module's writers, not of the module it is being aligned with. As with 0052 and
	// autoquarantine_mode/autopause_enabled, none of the pre-existing booleans is dropped: they are
	// composed as CEILINGs) = 89. The absence
	// assertions below are the anti-vacuity companions to this count: a count alone could be satisfied
	// by dropping the WRONG keys.
	// PLUS ONE at 0055 (smtp_host_allow_cidrs, Phase 3 close-out C4: the SMTP host destination guard's
	// allowlist, default EMPTY) = 90.
	if n := configKeyCount(t, admin); n != 90 {
		t.Fatalf("after Up: send_byo.config has %d keys, want 90 (77 pre-Phase-7 + 10 Phase 7 seeds - 2 dropped at 0049 - 1 dropped at 0051 + 1 seeded at 0052 + 4 seeded at 0053 + 1 seeded at 0055)", n)
	}
	// send_byo_0055's seed is EMPTY by default and by default_value: a freshly migrated deployment must not
	// start with any destination range relaxed (the guard is on everywhere until a test harness says otherwise).
	var allowEmpty bool
	if err := admin.QueryRow(ctx, `SELECT value = '{"v":[]}'::jsonb AND default_value = '{"v":[]}'::jsonb AND value_type = 'array<string>'
	                                 FROM send_byo.config WHERE key = 'smtp_host_allow_cidrs'`).Scan(&allowEmpty); err != nil || !allowEmpty {
		t.Fatalf("after Up: smtp_host_allow_cidrs is not seeded as an EMPTY array<string> with an empty default (send_byo_0055): %v empty=%v", err, allowEmpty)
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
			t.Fatalf("after Up: send_byo.config missing %q (send_byo_0020 Phase 8 seed not applied)", key)
		}
	}
	// Phase 4's ONE additive schema delta: the per-user claim index (send_byo_0015, CONCURRENTLY).
	// Proves the runner APPLIED a CONCURRENTLY migration (not a no-op) — anti-vacuity for the
	// non-transactional path fix.
	if !indexExists(t, admin, "jobs_claim_by_user_idx") {
		t.Fatal("after Up: send_byo.jobs_claim_by_user_idx missing (send_byo_0015 CONCURRENTLY not applied)")
	}
	// Phase 5's additive columns (send_byo_0016). Anti-vacuity that the ADD COLUMN migration ran.
	if !columnExists(t, admin, "accounts", "consecutive_failures") {
		t.Fatal("after Up: send_byo.accounts.consecutive_failures missing (send_byo_0016 not applied)")
	}
	if !columnExists(t, admin, "accounts", "throttle_paused_at") {
		t.Fatal("after Up: send_byo.accounts.throttle_paused_at missing (send_byo_0016 not applied)")
	}
	// Phase 8 Chunk 1's additive column (send_byo_0020): the per-attempt identity that lets the Control
	// reject a stale replay from a superseded attempt (P7-OD-6).
	if !columnExists(t, admin, "jobs", "attempt_token") {
		t.Fatal("after Up: send_byo.jobs.attempt_token missing (send_byo_0020 not applied)")
	}
	// Phase 9.5's operability + completeness config seeds (send_byo_0021) — anti-vacuity. Ten of the
	// original eleven; variation_zero_width_rate_pct is asserted ABSENT below instead, since 0049
	// deletes it (M3-P8-OD-8).
	for _, key := range []string{
		"dispatch_enabled", "autoquarantine_mode", "autopause_enabled", "variation_enabled",
		"alert_webhook_url", "health_metrics_window_sec", "brain_singleton_acquire_timeout_sec",
		"unit_price_credits", "variation_comment_intensity_rates",
		"variation_css_micro_params",
	} {
		if !configKeyExists(t, admin, key) {
			t.Fatalf("after Up: send_byo.config missing %q (send_byo_0021 Phase 9.5 seed not applied)", key)
		}
	}
	// 0049's two drops (M3-P8-OD-8): both keys were RESERVED with a Phase 8 expiry that came due and
	// neither ever had a genuine module-3 consumer. Asserting ABSENCE after a full Up is the
	// anti-vacuity check that 0049 actually ran — a DELETE leaves no schema object behind for a
	// column/index/function check to see, so the missing row IS the only observable evidence.
	for _, key := range []string{"retry_count_4xx", "variation_zero_width_rate_pct"} {
		if configKeyExists(t, admin, key) {
			t.Fatalf("after Up: send_byo.config still has %q (send_byo_0049 did not delete it)", key)
		}
	}
	// 0051's one drop (ruled 2026-08-05). Until 2026-08-05 this position asserted the OPPOSITE — that
	// score_alert_threshold was still PRESENT, because 0049 deliberately kept it and re-pointed its
	// RESERVED expiry at the future-work item FW-M3-12. That retarget is what the ruling reverses: an
	// expiry aimed at an open-ended item can never come due, so the marker was permanent in all but
	// name. The key is now dropped, and as with 0049 the missing row is the only observable evidence a
	// pure-DELETE migration ran at all.
	if configKeyExists(t, admin, "score_alert_threshold") {
		t.Fatal("after Up: send_byo.config still has score_alert_threshold (send_byo_0051 did not delete it)")
	}
	// send_byo_0052's one seed (fix-plan item 12, the graduated dispatch kill-switch ladder). A pure
	// INSERT into config leaves no schema object behind either, so the row itself is the only observable
	// evidence the migration ran — and unlike 0049/0051 the evidence here is PRESENCE plus the seeded
	// VALUE. The value matters more than the key's existence: seeding any rung other than `serving`
	// would ship a system that starts up halted, and seeding a value outside the four-rung enum would
	// make the Control degrade to accept_and_defer on its first tick forever. Both failures are silent
	// (no error, mail simply does not move), which is exactly the class this test exists to catch. Both
	// value and default_value are pinned: an operator PUT moves `value`, and `default_value` is what a
	// future reseed or a config-reference cross-check reads, so a migration that seeded only one of them
	// correctly would leave the other as the lie.
	if val, def := configValueAndDefault(t, admin, "dispatch_mode"); val != `{"v": "serving"}` || def != `{"v": "serving"}` {
		t.Fatalf("after Up: dispatch_mode value=%s default_value=%s, want both {\"v\": \"serving\"} (send_byo_0052 either did not run or seeded a rung that starts the system halted)", val, def)
	}
	// ...and the companion that 0052 did NOT drop dispatch_enabled. ADR-0017 records why: ADR-0016's
	// two-condition drop test fails condition (1) for that boolean (it has many live consumers), so it
	// is RETAINED and composed as a severity floor of accept_and_defer rather than replaced. A future
	// edit that "tidies up" the superseded-looking boolean would break Rule 5 and every existing
	// runbook step that names it; it fails here.
	if !configKeyExists(t, admin, "dispatch_enabled") {
		t.Fatal("after Up: send_byo.config has lost dispatch_enabled — 0052 adds the rung ladder and must RETAIN the boolean (Rule 5; ADR-0017: it is composed as a floor, not replaced, because ADR-0016's condition (1) fails for it)")
	}
	// send_byo_0053's FOUR seeds (fix-plan item 13, ADR-0018: a shadow rung for every autonomous
	// mutator). Same reasoning as 0052's assertion above — a pure config INSERT leaves no schema object
	// behind, so the row IS the evidence — with the direction of the danger REVERSED: dispatch_mode had
	// to be checked for seeding a rung that starts the system HALTED, and these have to be checked for
	// seeding a mode that starts the system ACTING. The whole safety claim of item 13 is that a freshly
	// migrated deployment mutates nothing until an operator promotes the dial, so `shadow` is not a
	// preference here, it is the property.
	//
	// This is the assertion that CANNOT be masked. The dispatch harness pins all four to `enforce` (the
	// Phase-5/7-era suites assert the real enforcing mechanics), so the in-package test can only assert
	// `default_value`. This test runs the raw migration chain with no harness anywhere in the picture, so
	// it is the only place the seeded `value` itself is observable. Both are pinned here for the same
	// reason 0052 pins both: an operator PUT moves `value`, a reseed reads `default_value`, and a
	// migration that got only one right would leave the other as the lie.
	for _, key := range []string{"autopause_mode", "retention_mode", "campaign_expiry_mode", "account_purge_mode"} {
		val, def := configValueAndDefault(t, admin, key)
		if val != `{"v": "shadow"}` || def != `{"v": "shadow"}` {
			t.Fatalf("after Up: %s value=%s default_value=%s, want both {\"v\": \"shadow\"} (send_byo_0053 either did not run or seeded a mode that ACTS on a freshly migrated deployment — for account_purge_mode that means destroying credentials before any operator has looked at a single shadow count)", key, val, def)
		}
		if vt := configValueType(t, admin, key); vt != "string" {
			t.Fatalf("after Up: %s value_type=%q, want \"string\" (the adminapi validator dispositions the three-way enum; a bool value_type would make the PUT path reject `shadow` outright)", key, vt)
		}
	}
	// ...and the companion that 0053 dropped NONE of the booleans and NONE of the rate/threshold keys
	// its four dials now gate. Every one of these has live consumers, so ADR-0016's condition (1) fails
	// for all of them exactly as it did for dispatch_enabled: autopause_enabled is composed as a CEILING
	// on autopause_mode, and the rate/window/batch keys still parameterize each sweep at `enforce`. A
	// future edit that "tidies up" the superseded-looking boolean, or that treats the sweep's tuning keys
	// as replaced by the mode, fails here.
	//
	// autoquarantine_mode is in this list even though it predates item 13: it is on the SAME ladder with
	// the same three values, which makes it the single most likely row for a "delete the mode dials"
	// edit to take by mistake. It belongs to send_byo_0051's predecessor, not to 0053.
	for _, key := range []string{
		"autopause_enabled", "autoquarantine_mode", "dispatch_mode", "dispatch_enabled",
		"retention_sweep_interval_ms", "retention_delete_batch_size",
		"campaign_expiry_sec", "campaign_expiry_sweep_interval_ms",
		"account_retention_window_sec", "account_purge_sweep_interval_ms", "account_purge_delete_batch_size",
	} {
		if !configKeyExists(t, admin, key) {
			t.Fatalf("after Up: send_byo.config has lost %q — 0053 adds four mode dials and must RETAIN every boolean and every tuning key they gate (Rule 5; the mode says WHETHER a sweep acts, never HOW it selects or how fast it runs)", key)
		}
	}
	// The over-delete guard 0049 used to get from the assertion above has MOVED, not evaporated — it is
	// now in the Down loop, at the window between Down 0051 and Down 0049, where score_alert_threshold
	// must be back while retry_count_4xx is still gone. That window is strictly stronger than a
	// presence check after Up: it pins WHICH migration owns WHICH row, so a 0051 that deleted all three
	// keys, or a 0049 that swept up the third, fails there rather than passing here.
	// send_byo_0054 (the suspension READ route's version counter). A pure ADD COLUMN, so the column is
	// the evidence the migration ran, and its DEFAULT is the property the route's "no row reads version
	// 0" contract leans on: a row an older Control wrote (or a direct seed that names no version) must
	// read 0, not NULL (the column is NOT NULL) and not 1. Asserted by inserting such a row and reading
	// it back, rather than by reading information_schema's default text, which would pass for a
	// default of `0::bigint` and fail for the equivalent `(0)` spelling.
	if !columnExists(t, admin, "user_suspensions", "version") {
		t.Fatal("after Up: send_byo.user_suspensions.version missing (send_byo_0054 not applied)")
	}
	if got := userSuspensionVersionAfterBareInsert(t, admin); got != 0 {
		t.Fatalf("after Up: a user_suspensions row inserted without a version reads version=%d, want 0 (send_byo_0054's DEFAULT is the contract's \"no counted transition yet\")", got)
	}
	if userSuspensionNegativeVersionAccepted(t, admin) {
		t.Fatal("after Up: user_suspensions accepted version = -1 (send_byo_0054's CHECK (version >= 0) is missing)")
	}
	// send_byo_0022's shared error-transition predicate — the single definition both job_events
	// triggers gate their error context on. Anti-vacuity that the migration ran: it is a
	// CREATE OR REPLACE of two existing function BODIES, so no column/index/config check can see
	// it; the one NEW object it adds is this function.
	if !functionExists(t, admin, "fn_job_event_is_error_transition") {
		t.Fatal("after Up: send_byo.fn_job_event_is_error_transition missing (send_byo_0022 not applied)")
	}
	// ...and that the replaced bodies actually GATE on it, which is the behavior 0022 exists for.
	// The predicate is IMMUTABLE SQL, so it can be evaluated directly: a success transition
	// (sending -> sent) is not an error, a requeue (assigned -> queued) is. Checking the predicate
	// rather than driving a job keeps this a migration test; the end-to-end "a SENT row carries no
	// error_class" assertion lives in the behavioral harness.
	if isErrorTransition(t, admin, "sending", "sent") {
		t.Fatal("after Up: fn_job_event_is_error_transition('sending','sent') is TRUE — a success would be tagged as an error (send_byo_0022 predicate wrong)")
	}
	if !isErrorTransition(t, admin, "assigned", "queued") {
		t.Fatal("after Up: fn_job_event_is_error_transition('assigned','queued') is FALSE — a requeue would lose its error context (send_byo_0022 predicate wrong)")
	}
	// send_byo_0023 (G11-OD-1 / ADR-0011 option B): the mid-life singleton re-acquire cadence. Its ONLY
	// object is one send_byo.config row, so existence alone would be a weak check — assert the seeded
	// value AND default_value, because Rule 1 requires every key to ship a documented default_value
	// and the whole point of the key is that the cadence is configured, not hardcoded. A seed that
	// landed with a NULL/absent default would satisfy configKeyExists and still violate Rule 1.
	if !configKeyExists(t, admin, "brain_singleton_reacquire_interval_sec") {
		t.Fatal("after Up: send_byo.config missing \"brain_singleton_reacquire_interval_sec\" (send_byo_0023 seed not applied)")
	}
	if v, d := configValueAndDefault(t, admin, "brain_singleton_reacquire_interval_sec"); v != `{"v": 5}` || d != `{"v": 5}` {
		t.Fatalf("after Up: brain_singleton_reacquire_interval_sec value=%s default_value=%s, want both {\"v\": 5} (send_byo_0023 seeded the wrong shape — the reader in main.go unwraps {\"v\": ...} and a bare 5 would silently fall back to 0 = never re-acquire)", v, d)
	}
	// send_byo_0024 (G12-OD-1 / ADR-0012 option A): job_events' append-only trigger gains the narrow
	// per-nullable-FK-column carve-out, so a parent DELETE's own `SET <col> = NULL` maintenance update
	// is permitted and the documented hard delete works again. This migration replaces ONE function
	// BODY and adds no object, so it is invisible to every existence check above — it has to be DRIVEN.
	// PAIRED, per Rule 10: the permitted half alone would report "landed" for a tree with the trigger
	// DROPPED, which is a strictly worse bug (a mutable audit log) than the wedge 0024 fixes.
	if !jobEventsCarveoutActive(t, admin) {
		t.Fatal("after Up: an ON DELETE SET NULL-shaped UPDATE on send_byo.job_events (account_id -> NULL) was REJECTED — send_byo_0024's carve-out did not land, so DELETE /v1/admin/accounts/{id} is still wedged for any account with job history (ADR-0012)")
	}
	if !jobEventsContentUpdateRejected(t, admin) {
		t.Fatal("after Up: a CONTENT update on send_byo.job_events (actor -> 'admin') was PERMITTED — send_byo_0024's carve-out is too wide, or the append-only trigger is gone entirely; the audit log is mutable")
	}

	// --- Module 3's own migrations, 0025-0028 (Docs/history/phase-1-schema-delta.md §6) ---
	// 0025: six new enum types + event_actor ADD VALUE 'user'.
	for _, typ := range []string{"quarantine_reason", "from_verification_state", "dmarc_policy",
		"suspend_reason", "notification_severity", "notification_state"} {
		if !enumTypeExists(t, admin, typ) {
			t.Fatalf("after Up: send_byo.%s enum type missing (0025 not applied)", typ)
		}
	}
	if !enumValueExists(t, admin, "event_actor", "user") {
		t.Fatal("after Up: send_byo.event_actor is missing value 'user' (0025 ADD VALUE not applied)")
	}
	// 0026: the Phase 1 §3.2/§4.4/§4.7 additive columns on accounts.
	for _, col := range []string{"quarantine_reason", "quarantined_at", "unquarantine_count_day",
		"unquarantine_reset_day", "from_verification_state", "from_dmarc_policy", "from_spf_aligned",
		"from_dmarc_aligned", "from_checked_at", "from_last_attempt_at", "from_last_attempt_error",
		"from_check_detail", "deleted_at", "deleted_by", "purge_after"} {
		if !columnExists(t, admin, "accounts", col) {
			t.Fatalf("after Up: send_byo.accounts.%s missing (0026 not applied)", col)
		}
	}
	// 0027: pool_id gone, accounts_user_fk is RESTRICT (M3-P1-OD-12), the new indexes exist.
	if columnExists(t, admin, "accounts", "pool_id") {
		t.Fatal("after Up: send_byo.accounts.pool_id still present (0027 did not drop it)")
	}
	if got := fkDeleteAction(t, admin, "accounts_user_fk"); got != "r" {
		t.Fatalf("after Up: accounts_user_fk ON DELETE action = %q, want %q (RESTRICT, M3-P1-OD-12 — the deliberately-restored tenant-erasure guard)", got, "r")
	}
	if !indexExists(t, admin, "accounts_user_host_username_unique") {
		t.Fatal("after Up: send_byo.accounts_user_host_username_unique missing (0027 not applied)")
	}
	if !indexExists(t, admin, "accounts_eligibility_idx") {
		t.Fatal("after Up: send_byo.accounts_eligibility_idx missing (0027 not applied)")
	}
	// 0028: send_byo.pools and pool_kind are gone; default_shared_pool_id's row is gone.
	if tableExists(t, admin, "pools") {
		t.Fatal("after Up: send_byo.pools still present (0028 did not drop it)")
	}
	if enumTypeExists(t, admin, "pool_kind") {
		t.Fatal("after Up: send_byo.pool_kind still present (0028 did not drop it)")
	}
	if configKeyExists(t, admin, "default_shared_pool_id") {
		t.Fatal("after Up: send_byo.config still has default_shared_pool_id (0028 did not delete the row, M3-P2-OD-1)")
	}

	// --- G3's own migrations, 0029-0034 (Docs/history/phase-2-strip-shared-pool.md §8.1 criterion 12) ---
	for _, tbl := range []string{"user_settings", "user_config_overrides", "user_suspensions", "campaigns", "notifications"} {
		if !tableExists(t, admin, tbl) {
			t.Fatalf("after Up: send_byo.%s missing (G3 not applied)", tbl)
		}
	}
	if !columnExists(t, admin, "campaigns", "clock_paused_at") {
		t.Fatal("after Up: send_byo.campaigns.clock_paused_at missing (0032 not applied)")
	}
	if !indexExists(t, admin, "campaigns_expiry_sweep_idx") {
		t.Fatal("after Up: send_byo.campaigns_expiry_sweep_idx missing (0032 not applied)")
	}
	if !indexExists(t, admin, "notifications_live_dedup_idx") {
		t.Fatal("after Up: send_byo.notifications_live_dedup_idx missing (0033 not applied)")
	}
	// 0050 (audit D5 / fix-plan item 9): the index the user-facing notifications read route pages on.
	// Neither 0033 index can serve it — the dedup index's second column is dedup_key and its partial
	// predicate excludes cleared rows; the pending index only covers state='pending', which is almost
	// no row in practice. Asserted here so "0050 ran" is a checked fact and not merely a tracking row.
	if !indexExists(t, admin, "notifications_user_first_seen_idx") {
		t.Fatal("after Up: send_byo.notifications_user_first_seen_idx missing (0050 not applied)")
	}
	if !columnExists(t, admin, "user_settings", "routing_strategy") {
		t.Fatal("after Up: send_byo.user_settings.routing_strategy missing (0029 not applied)")
	}
	if !columnExists(t, admin, "user_config_overrides", "send_delay_min_sec") {
		t.Fatal("after Up: send_byo.user_config_overrides.send_delay_min_sec missing (0030 not applied)")
	}
	if !indexExists(t, admin, "user_suspensions_active_idx") {
		t.Fatal("after Up: send_byo.user_suspensions_active_idx missing (0031 not applied)")
	}
	// 0034: the widened account_events_action_check accepts the three new module-3 actions.
	for _, action := range []string{"verified", "credential_updated", "purged"} {
		if !accountEventsActionAllowed(t, admin, action) {
			t.Fatalf("after Up: account_events_action_check rejects action %q (0034 did not widen it)", action)
		}
	}

	// --- Phase 3's own migrations, 0035-0036 (Docs/history/phase-3-user-owned-accounts.md §2.4/§7) ---
	if !enumTypeExists(t, admin, "credential_verification_state") {
		t.Fatal("after Up: send_byo.credential_verification_state enum type missing (0035 not applied)")
	}
	for _, col := range []string{"credential_verification_state", "credential_verified_at",
		"credential_verify_at", "credential_verify_code", "credential_verify_response"} {
		if !columnExists(t, admin, "accounts", col) {
			t.Fatalf("after Up: send_byo.accounts.%s missing (0035 not applied)", col)
		}
	}
	for _, key := range []string{"account_test_rate_limit_per_day", "account_count_cap_per_user",
		"account_retention_window_sec", "bulk_add_verify_interval_ms",
		"account_purge_sweep_interval_ms", "account_purge_delete_batch_size"} {
		if !configKeyExists(t, admin, key) {
			t.Fatalf("after Up: send_byo.config missing %q (0036 not applied)", key)
		}
	}
	// 0037 (M3-P3-OD-17/18): the narrow INSERT-only grant on public.job_items for test-send.
	if !hasTablePrivilege(t, admin, "send_byo_app", "public.job_items", "INSERT") {
		t.Fatal("after Up: send_byo_app lacks INSERT on public.job_items (0037 not applied)")
	}

	// 0039 (Phase 4 §6.1): the seven From-gate config seeds.
	for _, key := range []string{"from_gate_mode", "from_dns_resolver", "from_dns_lookup_timeout_ms",
		"from_dns_total_budget_ms", "from_dns_cache_ttl_sec", "from_dns_negative_cache_ttl_sec",
		"from_spf_lookup_limit_posture"} {
		if !configKeyExists(t, admin, key) {
			t.Fatalf("after Up: send_byo.config missing %q (0039 not applied)", key)
		}
	}
	// 0040 (Phase 4 §4.6): the two acknowledgment columns + the generated fingerprint column.
	for _, col := range []string{"from_warning_acknowledged_at", "from_warning_acknowledged_for", "from_verdict_fingerprint"} {
		if !columnExists(t, admin, "accounts", col) {
			t.Fatalf("after Up: send_byo.accounts.%s missing (0040 not applied)", col)
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
			t.Fatalf("after Up: send_byo.config missing %q (0042 not applied)", key)
		}
	}
	if configKeyExists(t, admin, "quarantine_threshold") {
		t.Fatal("after Up: send_byo.config still has quarantine_threshold (0042 did not delete it, M3-P5-OD-1)")
	}
	// 0043 (§1.3): the job_events account_id+created_at index the failure-rate window needs.
	if !indexExists(t, admin, "job_events_account_id_created_at_idx") {
		t.Fatal("after Up: send_byo.job_events_account_id_created_at_idx missing (0043 not applied)")
	}
	// 0044 (M3-P5-OD-7): the widened account_events_action_check accepts the suspend/unsuspend
	// audit actions.
	if !accountEventsActionAllowed(t, admin, "suspend") || !accountEventsActionAllowed(t, admin, "unsuspend") {
		t.Fatal("after Up: account_events_action_check rejects \"suspend\"/\"unsuspend\" (0044 not applied)")
	}
	// 0045 (Module 3 Phase 6, M3-P3-OD-6's aggregate test-button companion).
	if !configKeyExists(t, admin, "test_button_rate_limit_per_user_per_day") {
		t.Fatal("after Up: send_byo.config missing \"test_button_rate_limit_per_user_per_day\" (0045 not applied)")
	}
	if v, d := configValueAndDefault(t, admin, "test_button_rate_limit_per_user_per_day"); v != `{"v": 20}` || d != `{"v": 20}` {
		t.Fatalf("after Up: test_button_rate_limit_per_user_per_day value=%s default_value=%s, want both {\"v\": 20} (0045 seeded the wrong shape)", v, d)
	}
	// 0046 (Phase 7 §1.4/§8): jobs.last_smtp_response — the slug-free column §1.4's downstream
	// no-derivation invariant needs.
	if !columnExists(t, admin, "jobs", "last_smtp_response") {
		t.Fatal("after Up: send_byo.jobs.last_smtp_response missing (0046 not applied)")
	}
	// 0046 (Phase 7 §7/M3-P7-OD-6): job_events.destination_domain, denormalized so the throttle
	// aggregation does not depend on send_logs' retention.
	if !columnExists(t, admin, "job_events", "destination_domain") {
		t.Fatal("after Up: send_byo.job_events.destination_domain missing (0046 not applied)")
	}
	if !indexExists(t, admin, "job_events_destination_domain_throttle_idx") {
		t.Fatal("after Up: job_events_destination_domain_throttle_idx missing (0046 not applied)")
	}
	// Behavioral, not just structural: a real state-change transition must populate
	// destination_domain from the job's recipient_email — proving the replaced trigger function
	// actually writes the column, not merely that the column exists.
	if got := insertJobAndTransition(t, admin, "carol@destcheck.example"); got != "destcheck.example" {
		t.Fatalf("after Up: job_events.destination_domain=%q on a state change for carol@destcheck.example, want \"destcheck.example\" (0046's trigger replacement not populating the column)", got)
	}
	// Anti-vacuity companion to Rule 9's own note: 0022's error-context gating fix must SURVIVE
	// 0046's replacement — a state change that leaves state UNCHANGED still fires no event
	// (early-return), and this genesis-vs-transition distinction is exactly what 0022 fixed. Not
	// re-asserted in full here (0022's own test covers it); this migration only replaces the
	// function body to add one more column, so the guard is that the early-return line is still
	// literally present in 0046's own SQL, checked at the file level by 0046's own header comment
	// citing 0022 as the base body — a source-level contract this test cannot verify at the SQL
	// level without duplicating 0022's entire test. Left as documented, not re-proven.
	// NINE of 0046's ten seeds, checked after a FULL Up. The tenth, score_alert_threshold, is asserted
	// ABSENT instead (0051 drops it — see that block above), which is why it cannot be in this list:
	// after the whole chain the row is gone, and requiring it here would contradict 0051. Its
	// value/default_value shape is still pinned, at Down 0051's restore window rather than here — that
	// is the only place in this test where the row legitimately exists.
	for _, key := range []string{
		"notification_digest_window_sec", "notification_dedup_window_sec",
		"notification_immediate_severities", "notifications_enabled",
		"notification_tick_interval_sec", "destination_throttle_window_sec",
		"destination_throttle_user_threshold", "quote_capacity_projection_enabled",
		"quote_long_duration_warn_sec",
	} {
		if !configKeyExists(t, admin, key) {
			t.Fatalf("after Up: send_byo.config missing %q (0046 not applied)", key)
		}
	}
	// 0047 (M3-P7-OD-10): notification_state gains 'suppressed', the digest-fold marker the delivery
	// loop needs (§3.2's T2: folded rows must be traceable, not silently absent). The three
	// pre-existing values (pending/delivered/cleared) are untouched by an ADD VALUE.
	if !enumValueExists(t, admin, "notification_state", "suppressed") {
		t.Fatal("after Up: send_byo.notification_state is missing value 'suppressed' (0047 not applied)")
	}
	for _, v := range []string{"pending", "delivered", "cleared"} {
		if !enumValueExists(t, admin, "notification_state", v) {
			t.Fatalf("after Up: send_byo.notification_state is missing pre-existing value %q — 0047's ADD VALUE must never remove one", v)
		}
	}
	// 0048 (M3-P7-OD-11): unit_price_credits re-seeded at 0 (flat-subscription pricing, not a
	// default_value mutation of the inherited send_byo_0021 row — that row is deleted and a fresh one
	// inserted, per Rule 1 and M3-P5-OD-1's own precedent).
	if v, d := configValueAndDefault(t, admin, "unit_price_credits"); v != `{"v": 0}` || d != `{"v": 0}` {
		t.Fatalf("after Up: unit_price_credits value=%s default_value=%s, want both {\"v\": 0} (0048 not applied, or seeded the wrong shape)", v, d)
	}
	// 0056/0057 (Phase 3 close-out C1, POST /v1/cancel). The enum value is last (the order describe publishes),
	// the six existing values are untouched, and the schema half is driven, not just read out of the catalog.
	if !enumValueExists(t, admin, "job_state", "cancelled") {
		t.Fatal("after Up: send_byo.job_state is missing value 'cancelled' (0056 not applied)")
	}
	for _, v := range []string{"queued", "assigned", "sending", "sent", "failed", "bounced"} {
		if !enumValueExists(t, admin, "job_state", v) {
			t.Fatalf("after Up: send_byo.job_state is missing pre-existing value %q -- 0056's ADD VALUE must never remove one", v)
		}
	}
	if got := jobStateOrder(t, admin); got != "queued,assigned,sending,sent,failed,bounced,cancelled" {
		t.Fatalf("after Up: job_state order = %q, want the six original values in place and cancelled LAST (describe.vocabularies.job_state publishes this order)", got)
	}
	if !columnExists(t, admin, "jobs", "cancel_requested_at") {
		t.Fatal("after Up: send_byo.jobs.cancel_requested_at missing (0057 not applied)")
	}
	cp := cancelGuardProbe(t, admin)
	if cp.flaggedState != "cancelled" || !cp.flaggedCompleted || !cp.flaggedLeaseCleared || cp.flaggedEvents != 1 {
		t.Fatalf("after Up: a FLAGGED job moved assigned->queued reads state=%q completed_at set=%v lease cleared=%v events to cancelled=%d, want cancelled/true/true/1 (0057's guard did not turn the requeue into a cancel)",
			cp.flaggedState, cp.flaggedCompleted, cp.flaggedLeaseCleared, cp.flaggedEvents)
	}
	// The paired control: the SAME requeue on a job nobody flagged must stay queued, or the assertion above
	// would pass for a guard that cancels every requeue.
	if cp.unflaggedState != "queued" {
		t.Fatalf("after Up: an UNFLAGGED job moved assigned->queued reads state=%q, want queued (the guard must act only on a row cancel flagged)", cp.unflaggedState)
	}
	if cp.flaggedEventErrorClass != "unknown" {
		t.Fatalf("after Up: the assigned->cancelled event of a failed in-flight attempt carries error_class %q, want \"unknown\" (0057 did not teach fn_job_event_is_error_transition that this is an error transition: the operator errors metric and the account failure window would stop counting a real failure)", cp.flaggedEventErrorClass)
	}
	if cp.queuedCancelEventErrorClass != "" {
		t.Fatalf("after Up: the queued->cancelled event of POST /v1/cancel carries error_class %q, want NULL (a cancel is not an error; a sticky earlier failure must not be stamped onto it)", cp.queuedCancelEventErrorClass)
	}
	// 0058 (Phase 3 close-out C5): the nullable plain-text column and its size CHECK, driven.
	assertTextMigration(t, admin)
	// 0059 (Phase 4 C1): the two pause/resume tables, their grants, key, CHECK, append-only guard and cascade, driven.
	assertCampaignPauseMigration(t, admin)
	// 0060 (Phase 5 S4): the nullable unsubscribe link column and its CHECK, driven on both sides.
	assertUnsubscribeMigration(t, admin)
	// 0061 (the owner's 30-day window): the re-seeded row, value and default_value both, and only that row.
	assertCampaignExpiryReseed(t, admin)
	if cp.cancelledWithoutCompletedAccepted {
		t.Fatal("after Up: jobs_completed_at_valid_check accepted state='cancelled' with completed_at NULL (0057 did not move cancelled to the terminal branch)")
	}
	if !cp.cancelledWithCompletedAccepted {
		t.Fatal("after Up: jobs_completed_at_valid_check rejected state='cancelled' with completed_at set (the control: the rejection above would otherwise be some other failure)")
	}

	// FORCE RLS is set on the crown-jewel credential table.
	if !accountsForceRLS(t, admin) {
		t.Fatal("after Up: send_byo.accounts does not have FORCE ROW LEVEL SECURITY")
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
	// --- 0057's down REFUSES while a `cancelled` row exists (its header says so): restoring the original
	// completed_at CHECK would have to rewrite or delete the record that the platform asked for a stop, and a
	// schema revert has no business doing that. Driven for real, BEFORE the loop reverses it: one committed
	// cancelled job, a Down that must fail and roll back WHOLE, then the row is removed and the loop proceeds. ---
	// 0057 is not the head of the chain any more (0058, 0059, 0060 and 0061 follow it), and the runner refuses to roll back a
	// version while a higher one is applied -- a refusal that would make the assertion below pass for the WRONG reason. So
	// 0061, 0060, 0059 and 0058 are reversed first (cleanly), and the refusal under test is checked to be the CHECK constraint's.
	for _, v := range []string{"0061", "0060"} {
		if err := r.Down(ctx, v); err != nil {
			t.Fatalf("Down %s (to make 0057 the head): %v", v, err)
		}
	}
	assertUnsubscribeDown(t, admin)
	assertCampaignExpiryDown(t, admin)
	if err := r.Down(ctx, "0059"); err != nil {
		t.Fatalf("Down 0059 (to make 0057 the head): %v", err)
	}
	assertCampaignPauseDown(t, admin)
	if err := r.Down(ctx, "0058"); err != nil {
		t.Fatalf("Down 0058 (to make 0057 the head): %v", err)
	}
	headChain := wantChain[:len(wantChain)-4]
	cancelledJob := seedCommittedCancelledJob(t, admin)
	if err := r.Down(ctx, "0057"); err == nil {
		t.Fatal("Down 0057 succeeded while a job is in state 'cancelled': it must refuse (and roll back) rather than rewrite or delete cancelled rows")
	} else if !strings.Contains(err.Error(), "jobs_completed_at_valid_check") {
		t.Fatalf("Down 0057 was refused for the wrong reason (want the restored jobs_completed_at_valid_check rejecting the cancelled row): %v", err)
	}
	if got := versions(t, admin); !equalStrings(got, headChain) {
		t.Fatalf("after the REFUSED Down 0057: versions = %v, want %v (the failed down did not roll back)", got, headChain)
	}
	if !columnExists(t, admin, "jobs", "cancel_requested_at") || !functionExists(t, admin, "fn_jobs_cancel_guard") {
		t.Fatal("after the REFUSED Down 0057: cancel_requested_at / fn_jobs_cancel_guard are gone (the failed down left a partial revert)")
	}
	// The paired control: with the cancelled row removed, the SAME down succeeds (so the refusal above was
	// the cancelled row and nothing else), then 0057 is re-applied so the loop below starts from the full chain.
	mustExec(t, admin, "DELETE FROM send_byo.jobs WHERE id = '"+cancelledJob+"'")
	if err := r.Down(ctx, "0057"); err != nil {
		t.Fatalf("Down 0057 with no cancelled row: %v (the control: the refusal above must be the cancelled row, not a broken down)", err)
	}
	if columnExists(t, admin, "jobs", "cancel_requested_at") || functionExists(t, admin, "fn_jobs_cancel_guard") {
		t.Fatal("after Down 0057 (no cancelled rows): cancel_requested_at or fn_jobs_cancel_guard is still present")
	}
	if err := r.Up(ctx); err != nil {
		t.Fatalf("re-Up after Down 0057, 0058, 0059, 0060 and 0061: %v", err)
	}
	if got := versions(t, admin); !equalStrings(got, wantChain) {
		t.Fatalf("after re-Up: versions = %v, want %v", got, wantChain)
	}

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
			t.Fatal("after Down 0020: send_byo.jobs.attempt_token is still present (down did not drop it)")
		}
		if v == "0020" && configKeyExists(t, admin, "variation_html_minify") {
			t.Fatal("after Down 0020: a Phase 8 content-variation config seed is still present (down did not delete it)")
		}
		if v == "0021" && configKeyExists(t, admin, "dispatch_enabled") {
			t.Fatal("after Down 0021: a Phase 9.5 operability config seed is still present (down did not delete it)")
		}
		if v == "0022" && functionExists(t, admin, "fn_job_event_is_error_transition") {
			t.Fatal("after Down 0022: send_byo.fn_job_event_is_error_transition is still present (down did not drop it)")
		}
		if v == "0023" && configKeyExists(t, admin, "brain_singleton_reacquire_interval_sec") {
			t.Fatal("after Down 0023: brain_singleton_reacquire_interval_sec is still present (down did not delete the seed)")
		}
		if v == "0037" && hasTablePrivilege(t, admin, "send_byo_app", "public.job_items", "INSERT") {
			t.Fatal("after Down 0037: send_byo_app still has INSERT on public.job_items (down did not revoke it)")
		}
		if v == "0038" && configKeyExists(t, admin, "bulk_add_max_rows") {
			t.Fatal("after Down 0038: send_byo.config still has bulk_add_max_rows (down did not delete it)")
		}
		if v == "0044" && (accountEventsActionAllowed(t, admin, "suspend") || accountEventsActionAllowed(t, admin, "unsuspend")) {
			t.Fatal("after Down 0044: account_events_action_check still accepts \"suspend\"/\"unsuspend\" (down did not narrow it)")
		}
		if v == "0045" && configKeyExists(t, admin, "test_button_rate_limit_per_user_per_day") {
			t.Fatal("after Down 0045: send_byo.config still has test_button_rate_limit_per_user_per_day (down did not delete it)")
		}
		if v == "0046" {
			if columnExists(t, admin, "jobs", "last_smtp_response") {
				t.Fatal("after Down 0046: send_byo.jobs.last_smtp_response still present (down did not drop it)")
			}
			if columnExists(t, admin, "job_events", "destination_domain") {
				t.Fatal("after Down 0046: send_byo.job_events.destination_domain still present (down did not drop it)")
			}
			if indexExists(t, admin, "job_events_destination_domain_throttle_idx") {
				t.Fatal("after Down 0046: job_events_destination_domain_throttle_idx still present (down did not drop it)")
			}
			// score_alert_threshold stays in this list even though 0051's up DELETEs it. The reverse chain
			// is what makes that correct: Down 0051 re-INSERTs the row (asserted above), so by the time
			// Down 0046 runs the row is present again and 0046 — its original seeder — is the file
			// responsible for removing it. Dropping it from this list would leave 0046's down free to
			// stop deleting a row it seeded, with nothing noticing.
			for _, key := range []string{
				"notification_digest_window_sec", "notification_dedup_window_sec",
				"notification_immediate_severities", "notifications_enabled",
				"notification_tick_interval_sec", "destination_throttle_window_sec",
				"destination_throttle_user_threshold", "quote_capacity_projection_enabled",
				"quote_long_duration_warn_sec", "score_alert_threshold",
			} {
				if configKeyExists(t, admin, key) {
					t.Fatalf("after Down 0046: send_byo.config still has %q (down did not delete it)", key)
				}
			}
		}
		// 0047's down is the SAME one-way-door asymmetry as 0025's 'user' (M3-P7-OD-10, M3-P1-OD-7
		// precedent): Postgres has no ALTER TYPE ... DROP VALUE, so 'suppressed' must SURVIVE in
		// notification_state even after this down — asserting a reversal here would be asserting
		// something that cannot happen.
		if v == "0047" && !enumValueExists(t, admin, "notification_state", "suppressed") {
			t.Fatal("after Down 0047: send_byo.notification_state is missing value 'suppressed' — impossible (Postgres has no DROP VALUE) unless the type itself was dropped/recreated, which 0047's down must not do")
		}
		// 0049's down re-INSERTs both dropped keys VERBATIM from their original seed sites (Rule 1:
		// default_value is never mutated) — a genuine reversal, the same shape as 0042's
		// quarantine_threshold restore. Asserting the exact default_value, not merely the key's
		// presence, is what catches a down file that "restores" an approximation: a one-way DELETE or
		// a re-seed at a guessed default would both leave a later re-Up() operating on a chain state
		// the forward path never produced.
		if v == "0049" {
			if val, def := configValueAndDefault(t, admin, "retry_count_4xx"); val != `{"v": 3}` || def != `{"v": 3}` {
				t.Fatalf("after Down 0049: retry_count_4xx value=%s default_value=%s, want both {\"v\": 3} (down did not restore the original send_byo_0010 row verbatim)", val, def)
			}
			// NOTE the space: the column is JSONB, so ::text renders canonically as {"v": 5} even
			// though send_byo_0021's literal was {"v":5} with none. Same reason 0048's assertion above
			// reads {"v": 1} against a {"v":1} literal.
			if val, def := configValueAndDefault(t, admin, "variation_zero_width_rate_pct"); val != `{"v": 5}` || def != `{"v": 5}` {
				t.Fatalf("after Down 0049: variation_zero_width_rate_pct value=%s default_value=%s, want both {\"v\": 5} (down did not restore the original send_byo_0021 row verbatim)", val, def)
			}
		}
		// 0048's down restores the ORIGINAL send_byo_0021 row verbatim ({"v":1}) — a genuine reversal,
		// unlike 0047's one-way-door enum value, since this migration only touches a table row.
		if v == "0048" {
			if v, d := configValueAndDefault(t, admin, "unit_price_credits"); v != `{"v": 1}` || d != `{"v": 1}` {
				t.Fatalf("after Down 0048: unit_price_credits value=%s default_value=%s, want both {\"v\": 1} (down did not restore the original send_byo_0021 row)", v, d)
			}
		}
		// 0051's down re-INSERTs score_alert_threshold VERBATIM from send_byo_0046's seed site (Rule 1:
		// default_value is never mutated), the same shape as 0049's and 0042's restores. Asserting the
		// exact value AND default_value rather than mere presence is what catches a "restore" that
		// approximates: a re-seed at a guessed default leaves a later re-Up() operating on a chain state
		// the forward path never produced.
		//
		// This block also carries the OVER-DELETE GUARD that used to live after Up as "0049 kept this
		// key." The Down loop runs in strict reverse, so at this point 0051 has been reversed and 0049
		// has NOT: score_alert_threshold must be BACK and retry_count_4xx must still be GONE. That pair
		// of opposite states in one window is what pins ownership — a 0051 that deleted all three keys,
		// or a 0049 that swept up the third, cannot satisfy both halves. A presence check after Up
		// could not distinguish those cases at all.
		if v == "0051" {
			if val, def := configValueAndDefault(t, admin, "score_alert_threshold"); val != `{"v": 30}` || def != `{"v": 30}` {
				t.Fatalf("after Down 0051: score_alert_threshold value=%s default_value=%s, want both {\"v\": 30} (down did not restore the original send_byo_0046 row verbatim)", val, def)
			}
			if configKeyExists(t, admin, "retry_count_4xx") {
				t.Fatal("after Down 0051: retry_count_4xx is present, but 0049's down has not run yet — 0051 must own exactly its own row, and 0049 must own the other two (an over-broad down in either file lands here)")
			}
		}
		// The paired half of 0052's up-assertion. A seed migration reverses by DELETing its row, and
		// nothing else about it is observable, so without this a 0052 down that only removed its own
		// schema_migrations entry would pass every other check in this loop — leaving a key in the live
		// config table that the manifest (post-revert) no longer lists, which is precisely the state
		// config_drift exists to catch and would then catch on a clean restore instead of here.
		//
		// The over-delete half matters too, and for a sharper reason than usual: dispatch_enabled sits
		// right next to this row conceptually, is the thing 0052 supersedes in spirit, and is EXACTLY
		// what an over-broad down file would sweep up ("remove the dispatch switches this migration
		// introduced"). Deleting it would leave a system with no kill-switch at all after a revert.
		if v == "0052" {
			if configKeyExists(t, admin, "dispatch_mode") {
				t.Fatal("after Down 0052: send_byo.config still has dispatch_mode (down did not delete its seeded row)")
			}
			if !configKeyExists(t, admin, "dispatch_enabled") {
				t.Fatal("after Down 0052: dispatch_enabled is GONE — 0052's down must remove exactly its own row; sweeping up the retained boolean leaves a reverted system with no dispatch kill-switch at all")
			}
		}
		// The paired half of 0053's up-assertion, same shape as 0052's and the same reason: a seed
		// migration is only observable through its rows, so a down that removed only its schema_migrations
		// entry would pass every other check in this loop and leave four keys the post-revert manifest no
		// longer lists.
		//
		// The over-delete half is LONGER here than at 0052 and deliberately so. 0053 introduces four dials
		// that each sit next to a boolean or a set of tuning keys they gate, and an over-broad down written
		// as "remove the mutator switches this migration introduced" would take any of them:
		//   - autopause_enabled     — gated by autopause_mode as a CEILING; deleting it leaves a reverted
		//                             system with no way to turn auto-pause off at all
		//   - autoquarantine_mode   — on the SAME three-value ladder, which makes it the single most likely
		//                             row for a "delete the mode dials" revert to take by mistake. It
		//                             belongs to the Phase 9.5 seed, not to 0053, and this is the window
		//                             that pins that ownership: 0053 has been reversed here and the Phase
		//                             9.5 migration has NOT, so autoquarantine_mode must still be present.
		//   - dispatch_mode         — belongs to 0052, which has ALSO not been reversed yet at this point
		//                             (strict reverse order), so it must still be present too
		//   - the sweep tuning keys — retention/expiry/purge rates, windows and batch sizes. The mode says
		//                             WHETHER a sweep acts, never how it selects or how fast it runs, so
		//                             none of them is 0053's to remove.
		if v == "0053" {
			for _, key := range []string{"autopause_mode", "retention_mode", "campaign_expiry_mode", "account_purge_mode"} {
				if configKeyExists(t, admin, key) {
					t.Fatalf("after Down 0053: send_byo.config still has %q (down did not delete its seeded row)", key)
				}
			}
			for _, key := range []string{
				"autopause_enabled", "autoquarantine_mode", "dispatch_mode", "dispatch_enabled",
				"retention_sweep_interval_ms", "retention_delete_batch_size",
				"campaign_expiry_sec", "campaign_expiry_sweep_interval_ms",
				"account_retention_window_sec", "account_purge_sweep_interval_ms", "account_purge_delete_batch_size",
			} {
				if !configKeyExists(t, admin, key) {
					t.Fatalf("after Down 0053: %q is GONE — 0053's down must remove exactly its own four rows. autoquarantine_mode and dispatch_mode belong to EARLIER migrations that have not been reversed yet at this point in the loop, and every boolean/tuning key it gates has live consumers of its own", key)
				}
			}
		}
		// 0061's down puts the ORIGINAL 0042 row back, verbatim (7 days); 0060's down drops the unsubscribe column and its CHECK and
		// nothing else. (Both were already driven once above, when they were reversed to make 0057 the head; this is the second pass.)
		if v == "0061" {
			assertCampaignExpiryDown(t, admin)
		}
		if v == "0060" {
			assertUnsubscribeDown(t, admin)
		}
		// 0059's down drops its two tables and the append-only guard's function, and nothing else (the neighbours survive).
		if v == "0059" {
			assertCampaignPauseDown(t, admin)
		}
		// 0057's down removes the column, the guard trigger and its function, and restores the original
		// completed_at CHECK; 0056's own down is the documented NO-OP for the enum value (Postgres has no
		// DROP VALUE and the enum is additive-only forever), so 'cancelled' must SURVIVE both -- asserting
		// that it vanished would be asserting something that cannot happen. The paired half of each up.
		// 0058's down drops the column AND its size CHECK; the table (0007's) and every other jobs column survive.
		if v == "0058" {
			if columnExists(t, admin, "jobs", "rendered_text") {
				t.Fatal("after Down 0058: send_byo.jobs.rendered_text is still present (down did not drop it)")
			}
			if constraintExists(t, admin, "jobs", "jobs_rendered_text_size_check") {
				t.Fatal("after Down 0058: jobs_rendered_text_size_check is still present (down did not drop it)")
			}
			if !columnExists(t, admin, "jobs", "rendered_body") || !columnExists(t, admin, "jobs", "cancel_requested_at") {
				t.Fatal("after Down 0058: a column that is not 0058's is GONE (rendered_body / cancel_requested_at) -- the down must drop only rendered_text")
			}
		}
		if v == "0057" {
			if columnExists(t, admin, "jobs", "cancel_requested_at") {
				t.Fatal("after Down 0057: send_byo.jobs.cancel_requested_at still present (down did not drop it)")
			}
			if functionExists(t, admin, "fn_jobs_cancel_guard") {
				t.Fatal("after Down 0057: send_byo.fn_jobs_cancel_guard still present (down did not drop it)")
			}
			if !enumValueExists(t, admin, "job_state", "cancelled") {
				t.Fatal("after Down 0057: job_state lost 'cancelled' -- impossible without recreating the type, which 0057's down must not do (0056 owns the value)")
			}
			// the error-context predicate is back to 0022's body (cancelled is not an error transition again), and
			// still true for the requeue it always covered -- the control that the function was replaced, not dropped.
			if isErrorTransition(t, admin, "sending", "cancelled") {
				t.Fatal("after Down 0057: fn_job_event_is_error_transition still treats sending->cancelled as an error (down did not restore 0022's body)")
			}
			if !isErrorTransition(t, admin, "sending", "queued") {
				t.Fatal("after Down 0057: fn_job_event_is_error_transition no longer treats sending->queued as an error (down dropped or broke 0022's predicate instead of restoring it)")
			}
			// the ORIGINAL constraint is back: a `cancelled` state with completed_at set is no longer the
			// terminal branch, so it is refused (the reverse of the up-assertion).
			if cancelledWithCompletedAcceptedBare(t, admin) {
				t.Fatal("after Down 0057: jobs_completed_at_valid_check still accepts state='cancelled' with completed_at set (down did not restore the 0007 constraint)")
			}
		}
		if v == "0056" {
			if !enumValueExists(t, admin, "job_state", "cancelled") {
				t.Fatal("after Down 0056: job_state lost 'cancelled' -- 0056's down is a documented no-op for the value (Postgres cannot drop an enum value)")
			}
		}
		// The paired half of 0055's up-assertion: the seeded key is removed and ONLY that key (the count is
		// 89 again at the next step down, which the earlier 0053 window then re-checks by name).
		if v == "0055" {
			if configKeyExists(t, admin, "smtp_host_allow_cidrs") {
				t.Fatal("after Down 0055: send_byo.config still has smtp_host_allow_cidrs (down did not delete its seeded row)")
			}
			if n := configKeyCount(t, admin); n != 89 {
				t.Fatalf("after Down 0055: send_byo.config has %d keys, want 89 (0055's down must remove exactly its own row)", n)
			}
		}
		// The paired half of 0054's up-assertion: a column migration reverses completely, so the down
		// must actually drop the column. Without this a down file that only DELETEd its tracking row
		// would pass every other check in this loop, and a re-Up would then fail on a duplicate
		// column rather than here. The user_suspensions TABLE itself must survive (it is 0031's, which
		// has not been reversed yet at this point in the strict-reverse loop).
		if v == "0054" {
			if columnExists(t, admin, "user_suspensions", "version") {
				t.Fatal("after Down 0054: send_byo.user_suspensions.version still present (down did not drop it)")
			}
			if !tableExists(t, admin, "user_suspensions") {
				t.Fatal("after Down 0054: send_byo.user_suspensions is GONE — 0054's down must drop only its own column; the table belongs to 0031, which has not been reversed yet")
			}
		}
		// The paired half of 0050's up-assertion: an index migration reverses completely, so the down
		// must actually drop it. Without this, a down file that only DELETEd its tracking row would
		// pass every other check in this loop.
		if v == "0050" && indexExists(t, admin, "notifications_user_first_seen_idx") {
			t.Fatal("after Down 0050: send_byo.notifications_user_first_seen_idx still present (down did not drop it)")
		}
		if v == "0043" && indexExists(t, admin, "job_events_account_id_created_at_idx") {
			t.Fatal("after Down 0043: send_byo.job_events_account_id_created_at_idx still present (down did not drop it)")
		}
		if v == "0042" {
			for _, key := range []string{"quarantine_consecutive_threshold", "quarantine_failure_rate_pct",
				"quarantine_failure_window_sec", "quarantine_min_sample", "unquarantine_max_per_day",
				"unquarantine_evidence_ttl_sec", "max_campaign_size", "max_queued_jobs",
				"campaign_expiry_sec", "campaign_expiry_sweep_interval_ms"} {
				if configKeyExists(t, admin, key) {
					t.Fatalf("after Down 0042: send_byo.config still has %q (down did not delete it)", key)
				}
			}
			// The down file re-inserts quarantine_threshold VERBATIM from its send_byo_0010 seed
			// (Rule 1: default_value never mutated) — proves the down is a real reverse, not a
			// one-way DELETE that would leave a later re-Up() re-seeding a key the chain never lost.
			if !configKeyExists(t, admin, "quarantine_threshold") {
				t.Fatal("after Down 0042: send_byo.config is missing quarantine_threshold (down did not restore it)")
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
					t.Fatalf("after Down 0039: send_byo.config still has %q (down did not delete it)", key)
				}
			}
		}
		if v == "0036" {
			for _, key := range []string{"account_test_rate_limit_per_day", "account_count_cap_per_user",
				"account_retention_window_sec", "bulk_add_verify_interval_ms",
				"account_purge_sweep_interval_ms", "account_purge_delete_batch_size"} {
				if configKeyExists(t, admin, key) {
					t.Fatalf("after Down 0036: send_byo.config still has %q (down did not delete it)", key)
				}
			}
		}
		if v == "0035" {
			for _, col := range []string{"credential_verification_state", "credential_verified_at",
				"credential_verify_at", "credential_verify_code", "credential_verify_response"} {
				if columnExists(t, admin, "accounts", col) {
					t.Fatalf("after Down 0035: send_byo.accounts.%s still present (down did not drop it)", col)
				}
			}
			if enumTypeExists(t, admin, "credential_verification_state") {
				t.Fatal("after Down 0035: send_byo.credential_verification_state enum type still present (down did not drop it)")
			}
		}
		if v == "0034" && accountEventsActionAllowed(t, admin, "purged") {
			t.Fatal("after Down 0034: account_events_action_check still permits 'purged' — down did not restore the 0014 CHECK")
		}
		if v == "0033" && tableExists(t, admin, "notifications") {
			t.Fatal("after Down 0033: send_byo.notifications still present (down did not drop it)")
		}
		if v == "0032" && tableExists(t, admin, "campaigns") {
			t.Fatal("after Down 0032: send_byo.campaigns still present (down did not drop it)")
		}
		if v == "0031" && tableExists(t, admin, "user_suspensions") {
			t.Fatal("after Down 0031: send_byo.user_suspensions still present (down did not drop it)")
		}
		if v == "0030" && tableExists(t, admin, "user_config_overrides") {
			t.Fatal("after Down 0030: send_byo.user_config_overrides still present (down did not drop it)")
		}
		if v == "0029" && tableExists(t, admin, "user_settings") {
			t.Fatal("after Down 0029: send_byo.user_settings still present (down did not drop it)")
		}
		if v == "0028" {
			if !tableExists(t, admin, "pools") {
				t.Fatal("after Down 0028: send_byo.pools is missing (down did not re-create it)")
			}
			if !enumTypeExists(t, admin, "pool_kind") {
				t.Fatal("after Down 0028: send_byo.pool_kind is missing (down did not re-create it)")
			}
			if !configKeyExists(t, admin, "default_shared_pool_id") {
				t.Fatal("after Down 0028: send_byo.config is missing default_shared_pool_id (down did not re-insert the row)")
			}
		}
		if v == "0027" {
			if !columnExists(t, admin, "accounts", "pool_id") {
				t.Fatal("after Down 0027: send_byo.accounts.pool_id is missing (down did not re-add it)")
			}
			if got := fkDeleteAction(t, admin, "accounts_user_fk"); got != "c" {
				t.Fatalf("after Down 0027: accounts_user_fk ON DELETE action = %q, want %q (CASCADE restored) — the down must reverse the FK action too, or a rollback silently leaves the RESTRICT guard in place (M3-P1-OD-12)", got, "c")
			}
		}
		if v == "0026" {
			for _, col := range []string{"quarantine_reason", "from_verification_state", "deleted_at"} {
				if columnExists(t, admin, "accounts", col) {
					t.Fatalf("after Down 0026: send_byo.accounts.%s is still present (down did not drop it)", col)
				}
			}
		}
		// 0025's down is the documented ONE-WAY DOOR (M3-P1-OD-7): Postgres has no
		// ALTER TYPE ... DROP VALUE, so 'user' must SURVIVE in event_actor even after this down —
		// asserting a reversal here would be asserting something that cannot happen. The six new
		// types it CAN drop must actually be gone.
		if v == "0025" {
			if !enumValueExists(t, admin, "event_actor", "user") {
				t.Fatal("after Down 0025: send_byo.event_actor is missing value 'user' — impossible (Postgres has no DROP VALUE) unless the type itself was dropped/recreated, which 0025's down must not do")
			}
			for _, typ := range []string{"quarantine_reason", "from_verification_state", "dmarc_policy",
				"suspend_reason", "notification_severity", "notification_state"} {
				if enumTypeExists(t, admin, typ) {
					t.Fatalf("after Down 0025: send_byo.%s is still present (down did not drop it)", typ)
				}
			}
		}
		// 0024's down restores send_byo_0008's UNCONDITIONAL body, deliberately reinstating the wedge —
		// that is what "reversible" means here. Driving the probe is the only way to see it, and this
		// assertion is what proves the down is real rather than a version-row deletion.
		if v == "0024" && jobEventsCarveoutActive(t, admin) {
			t.Fatal("after Down 0024: an account_id -> NULL update on send_byo.job_events is STILL permitted (down did not restore send_byo_0008's unconditional reject-trigger)")
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
		t.Fatal("after Down 0000: send_byo schema still present")
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
	if err := c.QueryRow(context.Background(), "SELECT count(*) FROM send_byo.config").Scan(&n); err != nil {
		t.Fatalf("config count: %v", err)
	}
	return n
}

// insertJobAndTransition seeds a real public.users/job_items/send_byo.jobs row for recipient
// (behind the required FKs), transitions the job queued->assigned via a real actor-GUC'd UPDATE
// (the same real path fn_job_events_on_state_change fires on), and returns the destination_domain
// column 0046's replacement trigger stamped into the resulting job_events row — behavioral proof
// the replacement function actually WRITES the column, not merely that it exists.
func insertJobAndTransition(t *testing.T, c *pgx.Conn, recipient string) string {
	t.Helper()
	ctx := context.Background()
	userID := "aaaaaaaa-0046-0046-0046-aaaaaaaaaaaa"
	itemID := "bbbbbbbb-0046-0046-0046-bbbbbbbbbbbb"
	if _, err := c.Exec(ctx,
		`INSERT INTO public.users (id, email, is_active) VALUES ($1, 'j0046@example.com', true) ON CONFLICT (id) DO NOTHING`, userID); err != nil {
		t.Fatalf("insertJobAndTransition: seed user: %v", err)
	}
	if _, err := c.Exec(ctx,
		`INSERT INTO public.job_items (id, campaign_id, user_id, recipient_email, rendered_subject, rendered_body, reply_to, status)
		 VALUES ($1, $2, $2, $3, 's', 'b', 'r@x.com', 'pending') ON CONFLICT (id) DO NOTHING`,
		itemID, userID, recipient); err != nil {
		t.Fatalf("insertJobAndTransition: seed job_item: %v", err)
	}
	var jobID string
	if err := c.QueryRow(ctx,
		`INSERT INTO send_byo.jobs (job_item_id, campaign_id, user_id, recipient_email, rendered_subject, rendered_body, reply_to, state, next_attempt_at)
		 VALUES ($1, $2, $2, $3, 's', 'b', 'r@x.com', 'queued', NOW()) RETURNING id`,
		itemID, userID, recipient).Scan(&jobID); err != nil {
		t.Fatalf("insertJobAndTransition: seed job: %v", err)
	}
	// set_config(..., true) scopes the GUC to the CURRENT TRANSACTION — a bare connection Exec is
	// its own implicit transaction, so the setting would be gone before the next statement even
	// ran. Both statements must share one real transaction (mirrors this file's other set_config
	// call sites, all of which use tx.Exec for exactly this reason).
	tx, err := c.Begin(ctx)
	if err != nil {
		t.Fatalf("insertJobAndTransition: begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck — no-op after a successful Commit below
	if _, err := tx.Exec(ctx, `SELECT set_config('send_byo.actor', 'dispatcher', true)`); err != nil {
		t.Fatalf("insertJobAndTransition: set actor: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE send_byo.jobs SET state = 'assigned', lease_expires_at = NOW() + INTERVAL '5 minutes' WHERE id = $1`, jobID); err != nil {
		t.Fatalf("insertJobAndTransition: transition job: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("insertJobAndTransition: commit: %v", err)
	}
	var domain *string
	if err := c.QueryRow(ctx,
		`SELECT destination_domain FROM send_byo.job_events WHERE job_id = $1 AND to_state = 'assigned'`, jobID).Scan(&domain); err != nil {
		t.Fatalf("insertJobAndTransition: read destination_domain: %v", err)
	}
	if domain == nil {
		return ""
	}
	return *domain
}

// jobStateOrder returns the job_state labels in enumsortorder, comma-joined: the order
// describe.vocabularies.job_state publishes.
func jobStateOrder(t *testing.T, c *pgx.Conn) string {
	t.Helper()
	rows, err := c.Query(context.Background(),
		`SELECT e.enumlabel FROM pg_enum e JOIN pg_type ty ON ty.oid = e.enumtypid JOIN pg_namespace n ON n.oid = ty.typnamespace
		  WHERE n.nspname = 'send_byo' AND ty.typname = 'job_state' ORDER BY e.enumsortorder`)
	if err != nil {
		t.Fatalf("job_state order: %v", err)
	}
	defer rows.Close()
	var labels []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			t.Fatal(err)
		}
		labels = append(labels, l)
	}
	return strings.Join(labels, ",")
}

// seedCancelFixture inserts a throwaway tenant, one job_item per suffix and a queued send_byo.jobs row for
// each, inside tx, and returns the job ids in suffix order. Several jobs let the probe run a flagged job and
// its unflagged control against the same schema in one rolled-back transaction.
func seedCancelFixture(t *testing.T, ctx context.Context, tx pgx.Tx, suffixes ...string) []string {
	t.Helper()
	var userID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO public.users (email, is_active) VALUES ('j0057-cancel-probe@sender.local', true) RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("0057 probe: seed user: %v", err)
	}
	var ids []string
	for _, sfx := range suffixes {
		var itemID string
		if err := tx.QueryRow(ctx,
			`INSERT INTO public.job_items (campaign_id, user_id, recipient_email, rendered_subject, rendered_body, reply_to, status)
			 VALUES ($1, $1, $2, 's', 'b', 'r@x.com', 'pending') RETURNING id`,
			userID, "rcpt-"+sfx+"@probe0057.example").Scan(&itemID); err != nil {
			t.Fatalf("0057 probe: seed job_item %s: %v", sfx, err)
		}
		var jobID string
		if err := tx.QueryRow(ctx,
			`INSERT INTO send_byo.jobs (job_item_id, campaign_id, user_id, recipient_email, rendered_subject, rendered_body, reply_to, state, next_attempt_at)
			 VALUES ($1, $2, $2, $3, 's', 'b', 'r@x.com', 'queued', NOW()) RETURNING id`,
			itemID, userID, "rcpt-"+sfx+"@probe0057.example").Scan(&jobID); err != nil {
			t.Fatalf("0057 probe: seed job %s: %v", sfx, err)
		}
		ids = append(ids, jobID)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('send_byo.actor', 'dispatcher', true)`); err != nil {
		t.Fatalf("0057 probe: set actor: %v", err)
	}
	return ids
}

// cancelProbeResult is what cancelGuardProbe observed.
type cancelProbeResult struct {
	flaggedState                      string // a job flagged cancel_requested_at, moved assigned->queued
	flaggedCompleted                  bool
	flaggedLeaseCleared               bool
	flaggedEvents                     int    // job_events rows assigned->cancelled for that job
	flaggedEventErrorClass            string // error_class stamped on that assigned->cancelled event ("" = NULL)
	queuedCancelEventErrorClass       string // error_class on a queued->cancelled event of a job that failed earlier ("" = NULL)
	unflaggedState                    string // the same move on an unflagged job (the control)
	cancelledWithoutCompletedAccepted bool   // state='cancelled', completed_at NULL accepted by the CHECK?
	cancelledWithCompletedAccepted    bool   // state='cancelled', completed_at set accepted by the CHECK?
}

// cancelGuardProbe drives send_byo_0057 for real in a transaction it rolls back: the guard (flagged vs the
// unflagged control) and the widened completed_at CHECK (both directions).
func cancelGuardProbe(t *testing.T, c *pgx.Conn) cancelProbeResult {
	t.Helper()
	ctx := context.Background()
	tx, err := c.Begin(ctx)
	if err != nil {
		t.Fatalf("begin 0057 probe: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck -- nothing is committed; the probe leaves no rows
	ids := seedCancelFixture(t, ctx, tx, "flagged", "plain", "chk-bad", "chk-ok", "failed-then-cancelled")
	flagged, plain, chkBad, chkOK, failedThenCancelled := ids[0], ids[1], ids[2], ids[3], ids[4]

	for _, id := range []string{flagged, plain} {
		if _, err := tx.Exec(ctx,
			`UPDATE send_byo.jobs SET state = 'assigned', lease_expires_at = NOW() + INTERVAL '5 minutes' WHERE id = $1`, id); err != nil {
			t.Fatalf("0057 probe: claim %s: %v", id, err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE send_byo.jobs SET cancel_requested_at = NOW() WHERE id = $1`, flagged); err != nil {
		t.Fatalf("0057 probe: flag: %v", err)
	}
	// The requeue every in-flight failure path performs: state back to queued, lease cleared.
	for _, id := range []string{flagged, plain} {
		if _, err := tx.Exec(ctx,
			`UPDATE send_byo.jobs SET state = 'queued', lease_expires_at = NULL, next_attempt_at = NOW(),
			        last_error_class = 'unknown', last_error_message = 'probe' WHERE id = $1`, id); err != nil {
			t.Fatalf("0057 probe: requeue %s: %v", id, err)
		}
	}
	var res cancelProbeResult
	if err := tx.QueryRow(ctx,
		`SELECT state::text, completed_at IS NOT NULL, lease_expires_at IS NULL FROM send_byo.jobs WHERE id = $1`, flagged).
		Scan(&res.flaggedState, &res.flaggedCompleted, &res.flaggedLeaseCleared); err != nil {
		t.Fatalf("0057 probe: read flagged: %v", err)
	}
	if err := tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM send_byo.job_events WHERE job_id = $1 AND to_state = 'cancelled' AND from_state = 'assigned'`, flagged).
		Scan(&res.flaggedEvents); err != nil {
		t.Fatalf("0057 probe: read events: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT state::text FROM send_byo.jobs WHERE id = $1`, plain).Scan(&res.unflaggedState); err != nil {
		t.Fatalf("0057 probe: read unflagged: %v", err)
	}
	// The error context of the failed attempt's event: the guard's `assigned -> cancelled` row is an ERROR
	// transition (the attempt failed), so it carries the class; a plain `queued -> cancelled` -- the cancel
	// itself, of a job that failed once earlier and so still holds a sticky last_error_class -- is NOT, and must
	// not be stamped with that old failure (the defect send_byo_0022 fixed).
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(error_class::text, '') FROM send_byo.job_events WHERE job_id = $1 AND to_state = 'cancelled'`, flagged).
		Scan(&res.flaggedEventErrorClass); err != nil {
		t.Fatalf("0057 probe: read flagged event error class: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE send_byo.jobs SET last_error_class = 'unknown', last_error_message = 'earlier failure' WHERE id = $1`, failedThenCancelled); err != nil {
		t.Fatalf("0057 probe: give the job an earlier failure: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE send_byo.jobs SET state = 'cancelled', completed_at = NOW() WHERE id = $1`, failedThenCancelled); err != nil {
		t.Fatalf("0057 probe: cancel a queued job: %v", err)
	}
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(error_class::text, '') FROM send_byo.job_events WHERE job_id = $1 AND to_state = 'cancelled'`, failedThenCancelled).
		Scan(&res.queuedCancelEventErrorClass); err != nil {
		t.Fatalf("0057 probe: read queued-cancel event error class: %v", err)
	}

	// The CHECK, both directions, each in its own savepoint (a failed statement aborts a transaction).
	bad, err := tx.Begin(ctx)
	if err != nil {
		t.Fatalf("0057 probe: savepoint: %v", err)
	}
	_, badErr := bad.Exec(ctx, `UPDATE send_byo.jobs SET state = 'cancelled' WHERE id = $1`, chkBad)
	_ = bad.Rollback(ctx)
	res.cancelledWithoutCompletedAccepted = badErr == nil
	ok, err := tx.Begin(ctx)
	if err != nil {
		t.Fatalf("0057 probe: savepoint: %v", err)
	}
	_, okErr := ok.Exec(ctx, `UPDATE send_byo.jobs SET state = 'cancelled', completed_at = NOW() WHERE id = $1`, chkOK)
	_ = ok.Rollback(ctx)
	res.cancelledWithCompletedAccepted = okErr == nil
	return res
}

// cancelledWithCompletedAcceptedBare reports whether jobs_completed_at_valid_check accepts a row in state
// `cancelled` with completed_at set. After 0057 it does; after 0057's down (the 0007 text) it must not.
func cancelledWithCompletedAcceptedBare(t *testing.T, c *pgx.Conn) bool {
	t.Helper()
	ctx := context.Background()
	tx, err := c.Begin(ctx)
	if err != nil {
		t.Fatalf("begin 0057 down probe: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck -- nothing is committed
	ids := seedCancelFixture(t, ctx, tx, "down-probe")
	_, err = tx.Exec(ctx, `UPDATE send_byo.jobs SET state = 'cancelled', completed_at = NOW() WHERE id = $1`, ids[0])
	return err == nil
}

// seedCommittedCancelledJob commits one job in state `cancelled` (with its job_events history) and returns its
// id. The caller removes it (jobs -> job_events is ON DELETE CASCADE).
func seedCommittedCancelledJob(t *testing.T, c *pgx.Conn) string {
	t.Helper()
	ctx := context.Background()
	tx, err := c.Begin(ctx)
	if err != nil {
		t.Fatalf("begin committed cancelled job: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck -- a no-op after the Commit below
	ids := seedCancelFixture(t, ctx, tx, "committed")
	if _, err := tx.Exec(ctx,
		`UPDATE send_byo.jobs SET state = 'cancelled', completed_at = NOW() WHERE id = $1`, ids[0]); err != nil {
		t.Fatalf("committed cancelled job: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit cancelled job: %v", err)
	}
	return ids[0]
}

// columnExists reports whether send_byo.<table> has a column of the given name.
func columnExists(t *testing.T, c *pgx.Conn, table, column string) bool {
	t.Helper()
	var ok bool
	if err := c.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM information_schema.columns
		   WHERE table_schema='send_byo' AND table_name=$1 AND column_name=$2)`, table, column).Scan(&ok); err != nil {
		t.Fatalf("column exists lookup %q.%q: %v", table, column, err)
	}
	return ok
}

// userSuspensionVersionAfterBareInsert seeds a user and a user_suspensions row that names NO version,
// inside a transaction it rolls back, and returns the version that row reads. send_byo_0054's DEFAULT
// is what the suspension read route's "a row nobody has counted a transition for reads 0" rests on, so
// the default is driven rather than read out of information_schema.
func userSuspensionVersionAfterBareInsert(t *testing.T, c *pgx.Conn) int64 {
	t.Helper()
	ctx := context.Background()
	tx, err := c.Begin(ctx)
	if err != nil {
		t.Fatalf("begin 0054 default probe: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck — nothing is committed; the probe leaves no rows
	var userID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO public.users (email, is_active) VALUES ('susp0054-default-probe@sender.local', true) RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("0054 default probe: seed user: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO send_byo.user_suspensions (user_id, suspended, reason, suspended_at)
		 VALUES ($1, TRUE, 'operator', NOW())`, userID); err != nil {
		t.Fatalf("0054 default probe: insert a row with no version: %v", err)
	}
	var v int64
	if err := tx.QueryRow(ctx,
		`SELECT version FROM send_byo.user_suspensions WHERE user_id = $1`, userID).Scan(&v); err != nil {
		t.Fatalf("0054 default probe: read the version back: %v", err)
	}
	return v
}

// userSuspensionNegativeVersionAccepted is the paired NEGATIVE half of the default probe: it reports
// whether user_suspensions took a row at version = -1. A rejection must be send_byo_0054's own CHECK
// and nothing else, otherwise the probe is broken and the answer means nothing.
func userSuspensionNegativeVersionAccepted(t *testing.T, c *pgx.Conn) bool {
	t.Helper()
	ctx := context.Background()
	tx, err := c.Begin(ctx)
	if err != nil {
		t.Fatalf("begin 0054 check probe: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck — nothing is committed; the probe leaves no rows
	var userID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO public.users (email, is_active) VALUES ('susp0054-check-probe@sender.local', true) RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("0054 check probe: seed user: %v", err)
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO send_byo.user_suspensions (user_id, suspended, reason, suspended_at, version)
		 VALUES ($1, TRUE, 'operator', NOW(), -1)`, userID)
	if err == nil {
		return true
	}
	if !strings.Contains(err.Error(), "user_suspensions_version_check") {
		t.Fatalf("0054 check probe: the insert failed for an unexpected reason (the probe is broken, not the schema): %v", err)
	}
	return false
}

// configKeyExists reports whether a send_byo.config row with the given key is present (for the
// 0018-down anti-vacuity check: a Phase 6 seed must be gone after its down migration).
func configKeyExists(t *testing.T, c *pgx.Conn, key string) bool {
	t.Helper()
	var ok bool
	if err := c.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM send_byo.config WHERE key=$1)`, key).Scan(&ok); err != nil {
		t.Fatalf("config key exists lookup %q: %v", key, err)
	}
	return ok
}

// configValueAndDefault returns a config row's value and default_value as text. Used by the
// send_byo_0023 anti-vacuity check: that migration's only object is a config row, so proving it
// "landed" means proving the JSONB {"v": ...} wrapper and the Rule-1 default_value are both right —
// not merely that a row with the key exists.
func configValueAndDefault(t *testing.T, c *pgx.Conn, key string) (string, string) {
	t.Helper()
	var value, def string
	if err := c.QueryRow(context.Background(),
		`SELECT value::text, default_value::text FROM send_byo.config WHERE key=$1`, key).Scan(&value, &def); err != nil {
		t.Fatalf("config value lookup %q: %v", key, err)
	}
	return value, def
}

// configValueType returns a config key's value_type. Separate from configValueAndDefault because the
// mode dials (send_byo_0053) are the first keys where the DECLARED TYPE is itself load-bearing: the
// adminapi validator switches on value_type before it ever sees the three-way enum, so a dial seeded as
// `bool` would reject `shadow` on the PUT path while the migration's own seeded row read back fine.
func configValueType(t *testing.T, c *pgx.Conn, key string) string {
	t.Helper()
	var vt string
	if err := c.QueryRow(context.Background(),
		`SELECT value_type FROM send_byo.config WHERE key=$1`, key).Scan(&vt); err != nil {
		t.Fatalf("config value_type lookup %q: %v", key, err)
	}
	return vt
}

// indexExists reports whether an index of the given name exists in the send_byo schema.
func indexExists(t *testing.T, c *pgx.Conn, name string) bool {
	t.Helper()
	var ok bool
	if err := c.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname='send_byo' AND indexname=$1)`, name).Scan(&ok); err != nil {
		t.Fatalf("index exists lookup %q: %v", name, err)
	}
	return ok
}

// tableExists reports whether a base table of the given name exists in the send_byo schema.
func tableExists(t *testing.T, c *pgx.Conn, name string) bool {
	t.Helper()
	var ok bool
	if err := c.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM pg_tables WHERE schemaname='send_byo' AND tablename=$1)`, name).Scan(&ok); err != nil {
		t.Fatalf("table exists lookup %q: %v", name, err)
	}
	return ok
}

// enumTypeExists reports whether an enum (or any) type of the given name exists in the send_byo
// schema (module 3's 0025/0028 anti-vacuity — Docs/history/phase-1-schema-delta.md §6).
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
		   WHERE n.nspname='send_byo' AND ty.typname=$1)`, name).Scan(&ok); err != nil {
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
		   WHERE n.nspname='send_byo' AND ty.typname=$1 AND e.enumlabel=$2)`, enumType, value).Scan(&ok); err != nil {
		t.Fatalf("enum value exists lookup %s.%s: %v", enumType, value, err)
	}
	return ok
}

// accountEventsActionAllowed reports whether account_events_action_check's definition permits the
// given action literal — 0034's anti-vacuity (Docs/history/phase-2-strip-shared-pool.md §8.1 criterion 12):
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

// functionExists reports whether a function of the given name exists in the send_byo schema (for
// send_byo_0022, whose only NEW object is a function — its other two changes are CREATE OR REPLACE
// of bodies that already existed).
func functionExists(t *testing.T, c *pgx.Conn, name string) bool {
	t.Helper()
	var ok bool
	if err := c.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
		   WHERE n.nspname='send_byo' AND p.proname=$1)`, name).Scan(&ok); err != nil {
		t.Fatalf("function exists lookup %q: %v", name, err)
	}
	return ok
}

// jobEventsCarveoutActive reports whether send_byo_0024's FK-aware carve-out is in force, by DRIVING
// the trigger rather than by reading its source. send_byo_0024 replaces one function BODY and adds no
// object at all, so functionExists/columnExists/configKeyExists are all blind to it and a
// pg_get_functiondef text match would assert the migration's own SQL against itself (it would pass
// against a carve-out that was syntactically present but logically wrong).
//
// So: insert a throwaway job_events row with account_id set, attempt the exact mutation the FK's
// ON DELETE SET NULL maintenance statement performs, and report whether it was permitted. Wrapped in
// a transaction that is always ROLLED BACK, so the lifecycle DB is unchanged either way. Runs as the
// admin/superuser connection deliberately — send_byo_app has UPDATE revoked (send_byo_0008), so a
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
			`INSERT INTO send_byo.pools (user_id, kind, name) VALUES ($1,'shared','carveout-probe') RETURNING id`,
			userID).Scan(&poolID); err != nil {
			t.Fatalf("carve-out probe: seed pool: %v", err)
		}
		if err := tx.QueryRow(ctx,
			`INSERT INTO send_byo.accounts
			   (user_id, pool_id, smtp_host, smtp_port, smtp_username,
			    credential_ciphertext, credential_key_id, credential_nonce, from_address)
			 VALUES ($1,$2,'probe.invalid',587,'probe@ourdomain.com','\x00','k','\x00','probe@ourdomain.com')
			 RETURNING id`, userID, poolID).Scan(&acctID); err != nil {
			t.Fatalf("carve-out probe: seed account (pool_id shape): %v", err)
		}
	} else {
		if err := tx.QueryRow(ctx,
			`INSERT INTO send_byo.accounts
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
	if _, err := tx.Exec(ctx, `SELECT set_config('send_byo.actor','system',true)`); err != nil {
		t.Fatalf("carve-out probe: set actor: %v", err)
	}
	var jobID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO send_byo.jobs (job_item_id, campaign_id, user_id, recipient_email, rendered_subject, rendered_body, state, next_attempt_at)
		 VALUES ($1,$2,$2,'probe@example.com','S','<p>b</p>','queued',NOW()) RETURNING id`,
		jobItemID, userID).Scan(&jobID); err != nil {
		t.Fatalf("carve-out probe: seed job: %v", err)
	}
	var evID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO send_byo.job_events (job_id, user_id, from_state, to_state, actor, account_id)
		 VALUES ($1,$2,'queued','assigned','dispatcher',$3) RETURNING id`,
		jobID, userID, acctID).Scan(&evID); err != nil {
		t.Fatalf("carve-out probe: seed job_events row: %v", err)
	}

	// THE PROBE: exactly what `ON DELETE SET NULL` issues. Permitted only under send_byo_0024.
	permitted := true
	if _, err := tx.Exec(ctx, `UPDATE send_byo.job_events SET account_id=NULL WHERE id=$1`, evID); err != nil {
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
	if _, err := tx.Exec(ctx, `SELECT set_config('send_byo.actor','system',true)`); err != nil {
		t.Fatalf("content-reject probe: set actor: %v", err)
	}
	var jobID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO send_byo.jobs (job_item_id, campaign_id, user_id, recipient_email, rendered_subject, rendered_body, state, next_attempt_at)
		 VALUES ($1,$2,$2,'probe2@example.com','S','<p>b</p>','queued',NOW()) RETURNING id`,
		jobItemID, userID).Scan(&jobID); err != nil {
		t.Fatalf("content-reject probe: seed job: %v", err)
	}
	var evID string
	if err := tx.QueryRow(ctx,
		`SELECT id FROM send_byo.job_events WHERE job_id=$1 ORDER BY seq LIMIT 1`, jobID).Scan(&evID); err != nil {
		t.Fatalf("content-reject probe: read genesis event: %v", err)
	}
	_, err = tx.Exec(ctx, `UPDATE send_byo.job_events SET actor='admin' WHERE id=$1`, evID)
	return err != nil && strings.Contains(err.Error(), "append-only: UPDATE forbidden")
}

// isErrorTransition evaluates send_byo_0022's predicate directly. It is IMMUTABLE SQL over the
// job_state enum, so the migration's actual decision can be asserted without driving a job.
func isErrorTransition(t *testing.T, c *pgx.Conn, from, to string) bool {
	t.Helper()
	var ok bool
	if err := c.QueryRow(context.Background(),
		`SELECT send_byo.fn_job_event_is_error_transition($1::send_byo.job_state, $2::send_byo.job_state)`,
		from, to).Scan(&ok); err != nil {
		t.Fatalf("is_error_transition(%q,%q): %v", from, to, err)
	}
	return ok
}

func accountsForceRLS(t *testing.T, c *pgx.Conn) bool {
	t.Helper()
	var relforcerowsecurity bool
	err := c.QueryRow(context.Background(),
		"SELECT relforcerowsecurity FROM pg_class WHERE oid='send_byo.accounts'::regclass").Scan(&relforcerowsecurity)
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

// assertPlatformGrant asserts send_byo_app's exact privilege set on a public.* (platform-owned)
// table. want is comma-joined, alphabetically sorted to match information_schema's own
// string_agg ordering (e.g. "INSERT,SELECT") — callers must pass privileges in that order.
func assertPlatformGrant(t *testing.T, c *pgx.Conn, table, want string) {
	t.Helper()
	var priv string
	err := c.QueryRow(context.Background(),
		`SELECT string_agg(privilege_type, ',' ORDER BY privilege_type) FROM information_schema.role_table_grants
		 WHERE grantee='send_byo_app' AND table_schema='public' AND table_name=$1`, table).Scan(&priv)
	if err != nil {
		t.Fatalf("grant lookup public.%s: %v", table, err)
	}
	if priv != want {
		t.Fatalf("public.%s grants to send_byo_app = %q, want exactly %q", table, priv, want)
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
