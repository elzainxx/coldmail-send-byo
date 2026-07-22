// Package singleton implements the single-active-Brain guard (CLAUDE.md Rule 7).
//
// Mechanism: a SESSION-level Postgres advisory lock on a fixed key, held on a DEDICATED
// standalone connection (NOT pooled) for the Brain's lifetime. Session-level so Postgres
// auto-releases it when the connection dies — SIGKILL / host loss leaves NO stale lock.
//
// Ownership model (why this shape): a pgx.Conn is NOT safe for concurrent use, and readyz
// is called from HTTP goroutines. Rather than share the lock connection, a SINGLE background
// "keeper" goroutine exclusively owns the connection: it holds the lock and periodically
// verifies it with a lightweight query. readyz reads an atomic flag the keeper maintains —
// it never touches the connection. This is what makes the lock stable under a healthcheck
// that polls readyz every few seconds indefinitely (an earlier version pinged the lock conn
// from readyz, which raced and dropped the lock).
package singleton

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
)

// LockKey is the fixed, namespaced advisory-lock key for the Brain singleton. Distinct from
// the migration runner's key (9_100_000_001) — see docs/advisory-locks.md.
const LockKey int64 = 9_200_000_001

// ErrNotAcquired means another Brain holds the lock and the bounded wait elapsed. Callers
// treat this as a clean, expected exit — not a crash.
var ErrNotAcquired = errors.New("singleton: lock not acquired within timeout")

// Guard owns the dedicated lock connection via its keeper goroutine.
type Guard struct {
	conn    *pgx.Conn
	held    atomic.Bool
	stop    chan struct{}
	stopped chan struct{}
}

// Acquire opens a DEDICATED standalone connection (via dsn) and tries
// pg_try_advisory_lock(LockKey), polling until it succeeds or `timeout` elapses. timeout==0
// => a single non-blocking attempt (fail-fast). On success it starts the keeper goroutine
// that exclusively owns the connection and holds the lock for the process lifetime.
func Acquire(ctx context.Context, dsn string, timeout time.Duration) (*Guard, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("singleton dedicated connect: %w", err)
	}

	deadline := time.Now().Add(timeout)
	const backoff = 200 * time.Millisecond
	for {
		var got bool
		if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", LockKey).Scan(&got); err != nil {
			_ = conn.Close(ctx)
			return nil, fmt.Errorf("pg_try_advisory_lock: %w", err)
		}
		if got {
			g := &Guard{conn: conn, stop: make(chan struct{}), stopped: make(chan struct{})}
			g.held.Store(true)
			go g.keep()
			return g, nil
		}
		if timeout == 0 || time.Now().After(deadline) {
			_ = conn.Close(ctx)
			return nil, ErrNotAcquired
		}
		select {
		case <-ctx.Done():
			_ = conn.Close(ctx)
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
	}
}

// keep is the SOLE owner of the lock connection. It verifies the lock periodically with a
// trivial query. If the connection ever dies, it marks held=false (readyz then reports the
// truth) and exits — it does NOT try to silently re-acquire, because a dropped session lock
// means another Brain may have taken over, and re-acquiring would be a split-brain risk.
func (g *Guard) keep() {
	defer close(g.stopped)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-g.stop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			var one int
			err := g.conn.QueryRow(ctx, "SELECT 1").Scan(&one)
			cancel()
			if err != nil {
				log.Printf("singleton: lock connection lost (%v); marking not-held", err)
				g.held.Store(false)
				return
			}
		}
	}
}

// Healthy reports whether the lock is still held — read from an atomic flag the keeper
// maintains. It NEVER touches the lock connection, so concurrent readyz calls are safe and
// cannot disturb the lock.
func (g *Guard) Healthy(ctx context.Context) bool {
	return g != nil && g.held.Load()
}

// Release stops the keeper, releases the advisory lock, and closes the connection. On
// crash/SIGKILL this is unnecessary — Postgres auto-releases the session lock.
func (g *Guard) Release(ctx context.Context) {
	if g == nil || g.conn == nil {
		return
	}
	close(g.stop)
	<-g.stopped // keeper has released exclusive ownership of the connection
	g.held.Store(false)
	_, _ = g.conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", LockKey)
	_ = g.conn.Close(ctx)
	g.conn = nil
}
