package platformapi

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgxDB is the subset of pgxpool.Pool / pgx.Tx the account stores use, so a store function works
// against both a pool (single-statement reads) and a transaction (audited writes) — the same
// pattern as adminapi's own pgxDB (OD-7: a separate copy per package, not a shared import).
type pgxDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

var (
	_ pgxDB = (*pgxpool.Pool)(nil)
	_ pgxDB = (pgx.Tx)(nil)
)
