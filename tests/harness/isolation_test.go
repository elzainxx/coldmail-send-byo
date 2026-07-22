package harness

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestModeA_TemplateCloneIsRealAndCrossConnection proves Mode A gives REAL committed rows
// visible across DISTINCT connections — the property SKIP-LOCKED/concurrency tests depend on.
// Anti-vacuity: a second, independent connection must see a row the first committed, AND a
// control assertion proves the clone started empty (so the visible row is the one we wrote).
func TestModeA_TemplateCloneIsRealAndCrossConnection(t *testing.T) {
	adminDSN := AdminDSN(t)
	template := os.Getenv("TEST_TEMPLATE_DB")
	if template == "" {
		t.Skip("TEST_TEMPLATE_DB not set")
	}
	ctx := context.Background()

	cloneDSN, cleanup := CloneDB(t, adminDSN, template, "modea")
	defer cleanup()

	c1 := Connect(t, cloneDSN)
	c2 := Connect(t, cloneDSN) // a DISTINCT TCP session

	// Use a scratch table in the clone (no dependence on feature schema).
	if _, err := c1.Exec(ctx, "CREATE TABLE probe(id int primary key)"); err != nil {
		t.Fatalf("create probe: %v", err)
	}

	// Control: c2 sees zero rows before c1 commits one (proves the assertion below is real).
	var before int
	if err := c2.QueryRow(ctx, "SELECT count(*) FROM probe").Scan(&before); err != nil {
		t.Fatalf("count before: %v", err)
	}
	if before != 0 {
		t.Fatalf("anti-vacuity: probe not empty at start (%d)", before)
	}

	// c1 commits a row (autocommit Exec).
	if _, err := c1.Exec(ctx, "INSERT INTO probe(id) VALUES (1)"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// c2 (independent connection) MUST now see it — real committed cross-connection visibility.
	var after int
	if err := c2.QueryRow(ctx, "SELECT count(*) FROM probe").Scan(&after); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if after != 1 {
		t.Fatalf("Mode A broken: c2 sees %d rows, want 1 (cross-connection visibility failed)", after)
	}
}

// TestModeB_RollbackLeavesNoResidue proves Mode B isolates single-connection tests: work done
// inside the rolled-back tx is gone afterward. Anti-vacuity: assert the row EXISTED mid-tx.
func TestModeB_RollbackLeavesNoResidue(t *testing.T) {
	adminDSN := AdminDSN(t)
	template := os.Getenv("TEST_TEMPLATE_DB")
	if template == "" {
		t.Skip("TEST_TEMPLATE_DB not set")
	}
	ctx := context.Background()
	cloneDSN, cleanup := CloneDB(t, adminDSN, template, "modeb")
	defer cleanup()

	c := Connect(t, cloneDSN)
	if _, err := c.Exec(ctx, "CREATE TABLE probe(id int primary key)"); err != nil {
		t.Fatalf("create: %v", err)
	}

	sawInside := false
	WithRollbackTx(t, c, func(tx pgx.Tx) {
		_, _ = tx.Exec(ctx, "INSERT INTO probe(id) VALUES (7)")
		var n int
		_ = tx.QueryRow(ctx, "SELECT count(*) FROM probe").Scan(&n)
		sawInside = n == 1
	})

	if !sawInside {
		t.Fatal("anti-vacuity: row not visible inside the tx (test would be meaningless)")
	}
	// After rollback, the row is gone.
	var after int
	if err := c.QueryRow(ctx, "SELECT count(*) FROM probe").Scan(&after); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if after != 0 {
		t.Fatalf("Mode B broken: %d rows survived rollback, want 0", after)
	}
}
