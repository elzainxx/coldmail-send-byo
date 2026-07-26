package main

import (
	"context"
	"os"
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
		`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema='sender' AND table_name='schema_migrations')`).Scan(&ok)
	if err != nil {
		t.Fatalf("exists check: %v", err)
	}
	return ok
}

func versions(t *testing.T, c *pgx.Conn) []string {
	t.Helper()
	rows, err := c.Query(context.Background(), "SELECT version FROM sender.schema_migrations ORDER BY version")
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
	// roles sender_owner/sender_module are shared across databases (platform, template, this
	// one) and the bootstrap creates them idempotently (IF NOT EXISTS), so we do NOT drop
	// them here — dropping a role that still owns objects in another DB fails (2BP01). The
	// bootstrap's ownership/grant statements are idempotent against pre-existing roles.
	mustExec(t, admin, "DROP SCHEMA IF EXISTS sender CASCADE")

	// ANTI-VACUITY: assert the precondition. If the schema already existed here, the test
	// below would be meaningless — so prove it's genuinely gone before we apply.
	if schemaMigrationsExists(t, admin) {
		t.Fatal("precondition failed: sender.schema_migrations still exists after DROP — test would be vacuous")
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
	// sender_0014 (the sender.account_events audit sink). This is the executable expectation of
	// the migration chain — it grows as later phases add 0015+.
	wantChain := []string{"0000", "0001", "0002", "0003", "0004", "0005", "0006", "0007",
		"0008", "0009", "0010", "0011", "0012", "0013", "0014", "0015", "0016", "0017", "0018", "0019",
		"0020"}

	// --- Apply ---
	if err := r.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !schemaMigrationsExists(t, admin) {
		t.Fatal("after Up: sender.schema_migrations does not exist")
	}
	if got := versions(t, admin); !equalStrings(got, wantChain) {
		t.Fatalf("after Up: versions = %v, want %v", got, wantChain)
	}
	// Prove the real objects/roles/grants exist (not just the tracking rows). The roles,
	// schema ownership, and platform grants are established by the bootstrap (0000).
	assertRole(t, admin, "sender_owner", false /*login*/, false /*super*/)
	assertRole(t, admin, "sender_module", true /*login*/, false /*super*/)
	assertSchemaOwner(t, admin, "sender", "sender_owner")
	assertPlatformGrant(t, admin, "users")
	assertPlatformGrant(t, admin, "job_items")
	// Prove the Phase-1 feature objects exist and are owned by sender_owner (so FORCE RLS on
	// sender.accounts genuinely enforces — ADR-0004).
	for _, tbl := range []string{"pools", "accounts", "boxes", "jobs", "job_events", "send_logs", "config", "account_events"} {
		assertTableOwner(t, admin, "sender", tbl, "sender_owner")
	}
	// Config seeds the 14 foundation keys (sender_0010) + Phase 5's 3 (sender_0017) + Phase 6's 2
	// (sender_0018: reclaim_tick_interval_ms, smtp_operation_timeout_sec) + Phase 7's 6 (sender_0019:
	// startup_reconnect_window_sec, shutdown_drain_timeout_sec, retention_sweep_interval_ms,
	// retention_delete_batch_size, rollup_granularity, box_outcome_buffer_ttl_sec) + Phase 8's 14
	// (sender_0020, appended to the SAME version file as Chunk 1's attempt_token column — flagged
	// migration-filename deviation, Docs/open-decisions.md P8-OD-1) = 39.
	// Phase 4 added NO config key (§0); Phase 7 is fully schema-neutral (config seeds only, no table).
	if n := configKeyCount(t, admin); n != 39 {
		t.Fatalf("after Up: sender.config has %d keys, want 39 (14 foundation + 3 Phase 5 + 2 Phase 6 + 6 Phase 7 + 14 Phase 8)", n)
	}
	// Phase 8 Chunk 5's fourteen content-variation config seeds — anti-vacuity that they landed.
	for _, key := range []string{
		"variation_html_minify", "variation_comment_injection", "variation_comment_intensity",
		"variation_css_micro", "variation_unique_url_params", "variation_zero_width",
		"variation_encoding_rotation", "variation_mime_boundary_rotation", "variation_dynamic_preheader",
		"variation_preheader_templates", "variation_header_style", "variation_user_agent_injection",
		"variation_precedence", "variation_thread_index",
	} {
		if !configKeyExists(t, admin, key) {
			t.Fatalf("after Up: sender.config missing %q (sender_0020 Phase 8 seed not applied)", key)
		}
	}
	// Phase 4's ONE additive schema delta: the per-user claim index (sender_0015, CONCURRENTLY).
	// Proves the runner APPLIED a CONCURRENTLY migration (not a no-op) — anti-vacuity for the
	// non-transactional path fix.
	if !indexExists(t, admin, "jobs_claim_by_user_idx") {
		t.Fatal("after Up: sender.jobs_claim_by_user_idx missing (sender_0015 CONCURRENTLY not applied)")
	}
	// Phase 5's additive columns (sender_0016). Anti-vacuity that the ADD COLUMN migration ran.
	if !columnExists(t, admin, "accounts", "consecutive_failures") {
		t.Fatal("after Up: sender.accounts.consecutive_failures missing (sender_0016 not applied)")
	}
	if !columnExists(t, admin, "accounts", "throttle_paused_at") {
		t.Fatal("after Up: sender.accounts.throttle_paused_at missing (sender_0016 not applied)")
	}
	// Phase 8 Chunk 1's additive column (sender_0020): the per-attempt identity that lets the Brain
	// reject a stale replay from a superseded attempt (P7-OD-6).
	if !columnExists(t, admin, "jobs", "attempt_token") {
		t.Fatal("after Up: sender.jobs.attempt_token missing (sender_0020 not applied)")
	}
	// FORCE RLS is set on the crown-jewel credential table.
	if !accountsForceRLS(t, admin) {
		t.Fatal("after Up: sender.accounts does not have FORCE ROW LEVEL SECURITY")
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
			t.Fatal("after Down 0020: sender.jobs.attempt_token is still present (down did not drop it)")
		}
		if v == "0020" && configKeyExists(t, admin, "variation_html_minify") {
			t.Fatal("after Down 0020: a Phase 8 content-variation config seed is still present (down did not delete it)")
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
		t.Fatal("after Down 0000: sender schema still present")
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
	if err := c.QueryRow(context.Background(), "SELECT count(*) FROM sender.config").Scan(&n); err != nil {
		t.Fatalf("config count: %v", err)
	}
	return n
}

// columnExists reports whether sender.<table> has a column of the given name.
func columnExists(t *testing.T, c *pgx.Conn, table, column string) bool {
	t.Helper()
	var ok bool
	if err := c.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM information_schema.columns
		   WHERE table_schema='sender' AND table_name=$1 AND column_name=$2)`, table, column).Scan(&ok); err != nil {
		t.Fatalf("column exists lookup %q.%q: %v", table, column, err)
	}
	return ok
}

// configKeyExists reports whether a sender.config row with the given key is present (for the
// 0018-down anti-vacuity check: a Phase 6 seed must be gone after its down migration).
func configKeyExists(t *testing.T, c *pgx.Conn, key string) bool {
	t.Helper()
	var ok bool
	if err := c.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM sender.config WHERE key=$1)`, key).Scan(&ok); err != nil {
		t.Fatalf("config key exists lookup %q: %v", key, err)
	}
	return ok
}

// indexExists reports whether an index of the given name exists in the sender schema.
func indexExists(t *testing.T, c *pgx.Conn, name string) bool {
	t.Helper()
	var ok bool
	if err := c.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname='sender' AND indexname=$1)`, name).Scan(&ok); err != nil {
		t.Fatalf("index exists lookup %q: %v", name, err)
	}
	return ok
}

func accountsForceRLS(t *testing.T, c *pgx.Conn) bool {
	t.Helper()
	var relforcerowsecurity bool
	err := c.QueryRow(context.Background(),
		"SELECT relforcerowsecurity FROM pg_class WHERE oid='sender.accounts'::regclass").Scan(&relforcerowsecurity)
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

func assertPlatformGrant(t *testing.T, c *pgx.Conn, table string) {
	t.Helper()
	var priv string
	err := c.QueryRow(context.Background(),
		`SELECT string_agg(privilege_type, ',') FROM information_schema.role_table_grants
		 WHERE grantee='sender_module' AND table_schema='public' AND table_name=$1`, table).Scan(&priv)
	if err != nil {
		t.Fatalf("grant lookup public.%s: %v", table, err)
	}
	if priv != "SELECT" {
		t.Fatalf("public.%s grants to sender_module = %q, want exactly SELECT", table, priv)
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
