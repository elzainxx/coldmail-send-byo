package dispatch

import (
	"context"
	"log"
)

// Runner drives the dispatcher off a KICK signal — NOT a polling timer (a background poll loop is
// Phase 4, explicitly out of scope, §11). A kick arrives when `run` creates a job or when a box
// comes online. On each kick the runner drains queued work by calling TryDispatch until it
// pushes nothing more (so a burst of runs, or a box connecting to a backlog of one, is handled).
// Phase 3 is one-job-in-flight-per-box, so "drain" naturally stops at the first no-push.
type Runner struct {
	d    *Dispatcher
	kick chan struct{}
}

// NewRunner builds a Runner with a small buffered kick channel (coalescing: extra kicks while one
// is pending are dropped, since the loop re-drains on each wake).
func NewRunner(d *Dispatcher) *Runner {
	return &Runner{d: d, kick: make(chan struct{}, 1)}
}

// Kick signals the runner to attempt dispatch. Non-blocking (coalesces). Safe from any goroutine
// (the platform API's run handler, the box-online callback).
func (r *Runner) Kick() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// Run loops until ctx is cancelled, dispatching on each kick. It also does one initial attempt at
// startup (in case a job is already queued from a prior run and a box is connected).
func (r *Runner) Run(ctx context.Context) {
	r.drain(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.kick:
			r.drain(ctx)
		}
	}
}

// drain calls TryDispatch until it stops pushing (or errors). Bounded by the queue: each push
// consumes one queued job; when none is pushable (no queued job, no idle box, no eligible
// account) it returns.
func (r *Runner) drain(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		pushed, err := r.d.TryDispatch(ctx)
		if err != nil {
			log.Printf("dispatch: TryDispatch error: %v", err)
			return
		}
		if pushed == "" {
			return // nothing more to push right now
		}
	}
}
