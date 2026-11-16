package platformapi

// suspension_gate.go — the P2 close-out, owner ruling C3: suspension gates EVERYTHING that touches
// SMTP, not only campaign claims.
//
// THE GAP THIS CLOSES. A suspension stops the dispatcher CLAIMING a user's queued jobs and refuses
// /v1/run (store.go's createJob). It did not stop the other four routes that open an SMTP connection
// through a worker on the user's behalf: test-send (a real send forced to one account), the account
// test button (a real connect+STARTTLS+AUTH), verify-on-add inside account create, and the same
// verification inside bulk-add. A suspended user could therefore still drive traffic from the
// operator's worker IPs to a provider — the thing a suspension exists to stop.
//
// WHAT THE USER SEES. Exactly what /v1/run already shows a suspended user: HTTP 409, code `conflict`,
// and NO statement of why. The message here is curated and specific enough to be honest ("not
// available") without naming the mechanism: it must never contain the words suspend / suspended /
// suspension, and the response carries no details[]. The internal reason `user_suspended` is only an
// internalReason — logged against the request_id, never serialized (the two-vocabulary rule).
//
// WHERE THE GATE SITS, AND WHY IT IS NOT ONLY HERE. Each handler checks BEFORE it does anything with
// consequences (before the From-gate DNS lookup, the rate-limit accounting, the account-row write,
// the worker call). That is the part a user can see. The same predicate is ALSO enforced in the
// dispatch layer, at Verifier.Verify and Dispatcher.SendTest (dispatch/suspension_gate.go) — the two
// places that actually reach a worker, which every caller passes through, including the operator's
// break-glass account-create route that this package does not own (CLAUDE.md: gates belong in the
// store layer, where all writers pass). The two are not redundant: this one is what produces the
// user-facing answer and stops the pre-SMTP side effects; the dispatch one is what stops a call that
// raced a suspend landing between this check and the worker call, and a caller that never came through
// a handler here.
//
// ACCOUNT CREATE WHILE SUSPENDED IS REFUSED, not stored `unverified`. The existing code COULD store
// an account without SMTP traffic (a nil verifier leaves it `unverified`), but that would leave the
// user, on reinstatement, holding an account that is silently unroutable until they discover they must
// press Test; and it would let a suspended user keep mutating their account set. Refusing is the
// safer answer and the one the ruling prefers when in doubt: create is not available, the same
// sentence as the other three.

import (
	"context"
	"errors"
)

// ErrUserSuspended is what a TestSender or an AccountVerifier returns when the module refused to
// reach a worker because the user is suspended. main.go's adapters map dispatch's own sentinel onto
// it (platformapi cannot import internal/dispatch — OD-7 isolation — the same shape as
// ErrAccountIneligible). A handler that sees it answers the SAME opaque 409 as the pre-check does.
var ErrUserSuspended = errors.New("user is suspended")

// actionUnavailableMessage is the whole user-facing text of the refusal. It states that the ACTION is
// unavailable and nothing about why, and it deliberately avoids the words suspend / suspended /
// suspension (asserted by suspension_gate_test.go for every refused response).
const actionUnavailableMessage = "This action is not available for your account right now."

// userIsSuspended reports whether userID has an active suspension. It is the platform surface's one
// copy of the predicate dispatch/loop.go (notSuspendedFragment) and store.go (createJob) also state:
// "a row exists for the user with suspended = TRUE". Reason is deliberately not consulted — an
// operator suspension and a subscription_lapsed one are the same mechanism and the same gate.
func userIsSuspended(ctx context.Context, db pgxDB, userID string) (bool, error) {
	var suspended bool
	err := db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM send_byo.user_suspensions WHERE user_id=$1 AND suspended=TRUE)`,
		userID).Scan(&suspended)
	return suspended, err
}

// suspendedRefusal returns the opaque user-facing refusal when userID is suspended, an internal error
// when the check itself cannot be answered (FAIL CLOSED: a route that opens SMTP connections must not
// proceed on an unreadable suspension flag), and nil when the user may proceed.
func (s *Server) suspendedRefusal(ctx context.Context, userID string) *apiError {
	suspended, err := userIsSuspended(ctx, s.db, userID)
	if err != nil {
		return newErr(codeInternalError, "suspend_check_failed: "+err.Error())
	}
	if suspended {
		return userSuspendedError()
	}
	return nil
}

// userSuspendedError is the one place the refusal is built, so every route and every race-window
// mapping produces byte-identical output: 409 conflict, the curated message, no details.
func userSuspendedError() *apiError {
	return newErr(codeConflict, "user_suspended").withMessage(actionUnavailableMessage)
}
