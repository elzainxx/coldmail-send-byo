// Package harness provides real-infra test helpers: real Postgres isolation modes and typed
// factories that insert real committed rows. NEVER mocks the DB (CLAUDE.md Rule 3).
//
// Isolation modes (§4.2 of the Phase 0 spec):
//
//	A. Template-clone  — CREATE DATABASE test_<id> TEMPLATE <base>; real committed rows,
//	                     multiple connections, SKIP-LOCKED/advisory-lock/trigger semantics
//	                     exactly as prod. REQUIRED for any multi-connection test.
//	B. Txn-rollback    — BEGIN ... ROLLBACK around a single-connection test. Fast; safe
//	                     ONLY for single-connection shape/constraint checks.
package harness

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// AdminDSN is an admin/owner-capable DSN (superuser in dev) used to create/drop test DBs and
// observe truth. Tests SKIP when it is unset, so `go test` never silently passes on nothing.
func AdminDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_ADMIN_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_ADMIN_DATABASE_URL not set; skipping real-infra harness test")
	}
	return dsn
}

// Connect opens a single pgx connection (a distinct TCP session).
func Connect(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

// WithRollbackTx runs fn inside a transaction that is ALWAYS rolled back (Mode B). Single
// connection only — never use for concurrency tests (a rollback tx is invisible to other
// connections and would make a claim race undefined).
func WithRollbackTx(t *testing.T, c *pgx.Conn, fn func(tx pgx.Tx)) {
	t.Helper()
	ctx := context.Background()
	tx, err := c.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	fn(tx)
}

// CloneDB creates test_<suffix> from a TEMPLATE database (Mode A) and returns its DSN plus a
// cleanup that drops it. Rows committed here are real and visible across connections.
func CloneDB(t *testing.T, adminDSN, templateDB, suffix string) (string, func()) {
	t.Helper()
	ctx := context.Background()
	admin := Connect(t, adminDSN)
	name := fmt.Sprintf("test_%s", suffix)

	// Drop a stale clone if present, then clone from the template.
	_, _ = admin.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", quoteIdent(name)))
	if _, err := admin.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", quoteIdent(name), quoteIdent(templateDB))); err != nil {
		t.Fatalf("clone db: %v", err)
	}
	cloneDSN := replaceDBName(adminDSN, name)
	cleanup := func() {
		c := mustConnect(adminDSN)
		defer c.Close(context.Background())
		// Terminate stragglers, then drop.
		_, _ = c.Exec(context.Background(),
			"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid<>pg_backend_pid()", name)
		_, _ = c.Exec(context.Background(), fmt.Sprintf("DROP DATABASE IF EXISTS %s", quoteIdent(name)))
	}
	return cloneDSN, cleanup
}

func mustConnect(dsn string) *pgx.Conn {
	c, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		panic(err)
	}
	return c
}
