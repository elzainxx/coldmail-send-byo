// suspension_gate.go — the P2 close-out, owner ruling C3 (the dispatch-layer half): suspension gates
// everything that reaches a WORKER on a user's behalf, not only the claim of queued jobs.
//
// Before this file, a suspension stopped exactly one thing here: the dispatch loop claiming a user's
// queued jobs (loop.go's notSuspendedFragment). Verifier.Verify (connect + STARTTLS + AUTH, run for
// verify-on-add, bulk-add and the account test button, on the user surface AND the operator's
// break-glass account-create route) and Dispatcher.SendTest (a real send forced to one account) both
// pushed a frame to a worker with no look at the user's suspension at all.
//
// WHY HERE AND NOT ONLY IN THE HANDLERS. These two functions are the only places in the Control that
// hand a worker an SMTP session outside the dispatch loop, and every caller passes through one of
// them. A gate here covers the platform surface, the admin surface and any future caller by
// construction; a gate only in the handlers covers exactly the handlers someone remembered (CLAUDE.md:
// "gates belong in the STORE layer, where all writers pass. This has bitten three times"). The
// platform handlers ALSO check, up front, because they own the user-facing answer and must stop their
// own pre-SMTP side effects (From-gate DNS lookup, rate-limit accounting, account row) — see
// platformapi/suspension_gate.go. The two layers answer different questions and neither replaces the
// other.
//
// BEFORE ANY WORKER USE: the check is the first statement of each entry point, ahead of
// AnyConnectedWorker, the account read, the credential decrypt and the push. A refusal therefore
// touches the pusher zero times (asserted by suspension_gate_test.go against a counting pusher, with
// a paired control: the same call for an unsuspended user does reach it).
//
// FAIL CLOSED: a suspension flag that cannot be read refuses the call with the read error rather than
// proceeding. An SMTP session cannot be recalled, so on uncertainty the safe answer is no.
//
// This file only READS send_byo.user_suspensions — it adds no autonomous mutator, so it is not a
// mode-dial subject (mutator_mode_test.go's discovery looks for writes).
package dispatch

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrUserSuspended is returned by Verifier.Verify and Dispatcher.SendTest when the user is suspended.
// It is the internal sentinel; platformapi has its own (ErrUserSuspended there), which main.go's
// adapters map this one onto, and it is never serialized to a client.
var ErrUserSuspended = errors.New("user is suspended")

// userSuspendedSQL is the predicate loop.go's notSuspendedFragment, platformapi's createJob and
// platformapi's own gate all state: a row for the user with suspended = TRUE. The reason column is
// deliberately not consulted — 'operator' and 'subscription_lapsed' are one mechanism.
const userSuspendedSQL = `SELECT EXISTS(SELECT 1 FROM send_byo.user_suspensions WHERE user_id = $1 AND suspended = TRUE)`

// userSuspended reports whether userID currently has an active suspension.
func userSuspended(ctx context.Context, db *pgxpool.Pool, userID string) (bool, error) {
	var suspended bool
	if err := db.QueryRow(ctx, userSuspendedSQL, userID).Scan(&suspended); err != nil {
		return false, err
	}
	return suspended, nil
}
