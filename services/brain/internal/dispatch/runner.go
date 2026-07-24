package dispatch

import (
	"context"
	"log"
	"time"
)

// defaultTickIntervalOnLoadErr is used ONLY when a config read fails transiently (e.g. a brief DB
// blip) so the loop can retry rather than spin or die; it is never a substitute for the seeded
// dispatch_tick_interval_ms (config-not-code) — the very next successful Tick re-reads it fresh.
const defaultTickIntervalOnLoadErr = 1 * time.Second

// Runner drives the Phase-4 polling dispatch loop (§1.2): a single Brain ticks every
// dispatch_tick_interval_ms, re-reading config each time (Phase 2 OD-14 — no redeploy needed for a
// config change to take effect). Kick is an OPTIONAL responsiveness nudge — e.g. `run` creating a
// job, or a box coming online — that runs an EXTRA tick immediately instead of waiting out the
// remainder of the interval; the polling timer is the primary, always-on driver (locked: polling
// loop, not event-driven).
type Runner struct {
	d    *Dispatcher
	kick chan struct{}
}

// NewRunner builds a Runner with a small buffered kick channel (coalescing: extra kicks while one
// is pending are dropped, since the loop re-ticks on each wake anyway).
func NewRunner(d *Dispatcher) *Runner {
	return &Runner{d: d, kick: make(chan struct{}, 1)}
}

// Kick nudges the runner to tick immediately rather than wait for the timer. Non-blocking
// (coalesces). Safe from any goroutine (the platform API's run handler, the box-online callback).
func (r *Runner) Kick() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// Run loops until ctx is cancelled: tick, then sleep for the FRESHLY-READ dispatch_tick_interval_ms
// (or wake early on a Kick). It ticks once immediately at startup (in case work is already queued
// from a prior run) before entering the wait loop.
func (r *Runner) Run(ctx context.Context) {
	interval := r.tick(ctx)
	for {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-r.kick:
			timer.Stop()
			interval = r.tick(ctx)
		case <-timer.C:
			interval = r.tick(ctx)
		}
	}
}

// tick runs one Dispatcher.Tick and returns how long to wait before the next one, derived from the
// freshly-read config (dispatch_tick_interval_ms) so a config change takes effect on the very next
// wait, no redeploy (§0, Phase 2 OD-14). Recovers a panic from ANY single tick so an unexpected
// failure degrades to "skip this tick, retry on the next one" rather than crashing the whole
// singleton Brain — consistent with the kill-switch philosophy (CLAUDE.md Rule 7) that dispatch
// failures halt cleanly with zero job loss, never take the process down (a claimed-but-stuck job
// simply waits out its lease and is reclaimed by Phase 6; nothing here is unrecoverable).
func (r *Runner) tick(ctx context.Context) (interval time.Duration) {
	interval = defaultTickIntervalOnLoadErr
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("dispatch: tick PANIC recovered: %v — skipping this tick, will retry", rec)
		}
	}()
	cfg, pushed, err := r.d.Tick(ctx)
	if err != nil {
		log.Printf("dispatch: tick error: %v", err)
		return defaultTickIntervalOnLoadErr
	}
	if pushed > 0 {
		log.Printf("dispatch: tick pushed %d job(s)", pushed)
	}
	return time.Duration(cfg.DispatchTickIntervalMS) * time.Millisecond
}
