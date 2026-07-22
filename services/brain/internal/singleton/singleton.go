// Package singleton implements the single-active-Brain guard (CLAUDE.md Rule 7).
//
// Mechanism: a SESSION-level Postgres advisory lock on a fixed key, held on a dedicated
// connection for the Brain's entire lifetime. Session-level (not transaction-level) so it
// is auto-released by Postgres when the connection dies — SIGKILL / host loss leaves NO
// stale lock. A second Brain that cannot acquire within a bounded window exits cleanly.
package singleton

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sender-module/brain/internal/db"
)

// LockKey is the fixed, namespaced advisory-lock key for the Brain singleton. It is
// distinct from the migration runner's key (9_100_000_001) — see docs/advisory-locks.md.
const LockKey int64 = 9_200_000_001

// ErrNotAcquired means another Brain holds the lock and the bounded wait elapsed. Callers
// treat this as a clean, expected exit — not a crash.
var ErrNotAcquired = errors.New("singleton: lock not acquired within timeout")

// Guard holds the dedicated connection carrying the advisory lock.
type Guard struct {
	conn *pgx.Conn
}

// Acquire tries pg_try_advisory_lock(LockKey) on a dedicated connection, polling until it
// succeeds or `timeout` elapses. timeout==0 => a single non-blocking attempt (fail-fast).
// The bounded wait lets a normal rolling redeploy succeed (old Brain drains and releases,
// new Brain acquires) while still failing loudly on a genuine stuck singleton.
func Acquire(ctx context.Context, database *db.DB, timeout time.Duration) (*Guard, error) {
	conn, err := database.Pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire dedicated conn: %w", err)
	}
	// Hijack the underlying connection so the session (and thus the lock) lives for the
	// whole process, independent of the pool.
	raw := conn.Hijack()

	deadline := time.Now().Add(timeout)
	backoff := 200 * time.Millisecond
	for {
		var got bool
		if err := raw.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", LockKey).Scan(&got); err != nil {
			_ = raw.Close(ctx)
			return nil, fmt.Errorf("pg_try_advisory_lock: %w", err)
		}
		if got {
			return &Guard{conn: raw}, nil
		}
		if timeout == 0 || time.Now().After(deadline) {
			_ = raw.Close(ctx)
			return nil, ErrNotAcquired
		}
		select {
		case <-ctx.Done():
			_ = raw.Close(ctx)
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
	}
}

// Healthy reports whether the guard's connection is still alive (and thus the lock held).
func (g *Guard) Healthy(ctx context.Context) bool {
	if g == nil || g.conn == nil {
		return false
	}
	return g.conn.Ping(ctx) == nil
}

// Release explicitly releases the advisory lock and closes the connection. On crash/SIGKILL
// this is unnecessary — Postgres auto-releases the session lock when the backend dies.
func (g *Guard) Release(ctx context.Context) {
	if g == nil || g.conn == nil {
		return
	}
	_, _ = g.conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", LockKey)
	_ = g.conn.Close(ctx)
	g.conn = nil
}
