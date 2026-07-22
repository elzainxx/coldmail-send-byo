package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func mustConnectMig(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	c, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return c
}

// TestUp_ContiguityGap proves the runner HARD-ERRORS on a migration-chain gap (a file whose
// predecessor is neither applied nor on disk) rather than silently skipping it (Phase 0 spec
// §5.5). Uses a temp migrations dir with 0001 + 0003 (missing 0002) against the real DB.
// Anti-vacuity: also asserts the SAME dir with the gap filled (0002 present) does NOT error
// on the contiguity check — so the test fails if the guard is a no-op OR if it over-rejects.
func TestUp_ContiguityGap(t *testing.T) {
	dsn := testDSN(t) // skips if TEST_DATABASE_URL unset

	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Minimal, side-effect-free migrations: each only self-records its version into a scratch
	// table we create in 0001, so applying them can't collide with the real sender schema.
	write("sender_0001_probe.up.sql", "CREATE SCHEMA IF NOT EXISTS migtest_probe; CREATE TABLE IF NOT EXISTS migtest_probe.v(version text); INSERT INTO sender.schema_migrations(version) VALUES('0001');")
	write("sender_0001_probe.down.sql", "DELETE FROM sender.schema_migrations WHERE version='0001';")
	write("sender_0003_probe.up.sql", "INSERT INTO sender.schema_migrations(version) VALUES('0003');")
	write("sender_0003_probe.down.sql", "DELETE FROM sender.schema_migrations WHERE version='0003';")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The runner needs sender.schema_migrations to exist (normally created by the bootstrap).
	// Ensure it exists for this focused contiguity test without depending on prior state.
	setup := mustConnectMig(t, dsn)
	_, _ = setup.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS sender")
	_, _ = setup.Exec(ctx, "CREATE TABLE IF NOT EXISTS sender.schema_migrations (version text primary key, applied_at timestamptz default now())")
	// Start clean so the gap logic is deterministic.
	for _, v := range []string{"0001", "0002", "0003"} {
		_, _ = setup.Exec(ctx, "DELETE FROM sender.schema_migrations WHERE version=$1", v)
	}
	_, _ = setup.Exec(ctx, "DROP SCHEMA IF EXISTS migtest_probe CASCADE")
	setup.Close(ctx)

	r := &Runner{DSN: dsn, Dir: dir, PreflightPath: preflightPathForTest(), LockTimeout: "10s", StatementTimeout: "30s", SkipPreflight: true, AllowDown: true}

	err := r.Up(ctx)
	if err == nil {
		t.Fatal("expected a contiguity-gap error for 0001+0003 (missing 0002), got nil — guard is a no-op")
	}
	if !strings.Contains(err.Error(), "chain gap") {
		t.Fatalf("expected a 'chain gap' error, got: %v", err)
	}

	// Fill the gap: with 0002 present, the contiguity check must pass (guard doesn't over-reject).
	write("sender_0002_probe.up.sql", "INSERT INTO sender.schema_migrations(version) VALUES('0002');")
	write("sender_0002_probe.down.sql", "DELETE FROM sender.schema_migrations WHERE version='0002';")
	// Clean any rows a prior partial run left, so this is deterministic.
	cleanup := func() {
		c := mustConnectMig(t, dsn)
		defer c.Close(context.Background())
		for _, v := range []string{"0001", "0002", "0003"} {
			_, _ = c.Exec(context.Background(), "DELETE FROM sender.schema_migrations WHERE version=$1", v)
		}
		_, _ = c.Exec(context.Background(), "DROP SCHEMA IF EXISTS migtest_probe CASCADE")
	}
	cleanup()
	defer cleanup()

	if err := r.Up(ctx); err != nil {
		t.Fatalf("gap filled (0001+0002+0003) but Up still errored: %v", err)
	}
}
