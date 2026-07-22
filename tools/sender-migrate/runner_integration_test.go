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

	// --- Apply ---
	if err := r.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !schemaMigrationsExists(t, admin) {
		t.Fatal("after Up: sender.schema_migrations does not exist")
	}
	if got := versions(t, admin); len(got) != 1 || got[0] != "0000" {
		t.Fatalf("after Up: versions = %v, want [0000] (bootstrap is sender_0000)", got)
	}
	// Prove the real objects/roles/grants exist (not just the tracking row).
	assertRole(t, admin, "sender_owner", false /*login*/, false /*super*/)
	assertRole(t, admin, "sender_module", true /*login*/, false /*super*/)
	assertSchemaOwner(t, admin, "sender", "sender_owner")
	assertPlatformGrant(t, admin, "users")
	assertPlatformGrant(t, admin, "job_items")

	// --- Idempotent re-run: applying again changes nothing, still exactly [0000] ---
	if err := r.Up(ctx); err != nil {
		t.Fatalf("Up (2nd): %v", err)
	}
	if got := versions(t, admin); len(got) != 1 || got[0] != "0000" {
		t.Fatalf("after idempotent Up: versions = %v, want [0000]", got)
	}

	// --- Down: reverses cleanly ---
	if err := r.Down(ctx, "0000"); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if schemaMigrationsExists(t, admin) {
		t.Fatal("after Down: sender schema still present")
	}
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
