package adminapi

// user_suspension_read.go — the P2 close-out, owner ruling C2: GET /v1/admin/users/{id}/suspension.
//
// WHY IT EXISTS. Until this route, suspension state had NO reader anywhere: the two POST routes answer
// 200 on a change and 422 on "already in that state", and a caller that wants to know what state the
// module is in has to infer it from a status code — and cannot even tell an unknown user from a
// not-suspended one on unsuspend (both 422). The platform's reconcile loop reads this route back
// instead of treating a 422 as confirmation.
//
// THE RESPONSE (and nothing else — no reason text, no operator identity: the audit stays unreadable
// by design, and the stored reason column is always the enum 'operator' so it carries nothing a
// caller needs):
//
//	{"user_id": "<uuid>", "suspended": <bool>, "suspended_at": <RFC 3339 Nano UTC | null>,
//	 "reinstated_at": <RFC 3339 Nano UTC | null>, "version": <integer>}
//
// `suspended_at` is when the CURRENT or most recent suspension began; `reinstated_at` is when the most
// recent reinstate happened. Neither is cleared by the other transition, so a currently-suspended user
// who was reinstated once before reads both. `version` is the monotonic transition counter added by
// send_byo_0054 (see suspendUpsertSQL / unsuspendSQL: it moves in the SAME statement that flips the
// flag). A user who has never been suspended has no row and reads suspended=false, both timestamps
// null, version 0 — a known user in good standing is a 200, not a 404.
//
// ERRORS, deliberately the same as the sibling routes: a malformed id is 404 not_found (the siblings'
// own choice — an id that cannot name a user names none), an UNKNOWN user is 404 not_found, a wrong or
// missing operator key is 401 (requireAuth), a wrong verb is 405 (methodDispatch). Read-only, takes no
// body, writes no audit row, and — like its siblings — is not rate limited.
//
// TWO STATEMENTS, NOT ONE JOIN, ON PURPOSE. The existence check is userExists, the statement this
// package already runs for the suspend route; a single LEFT JOIN against the platform's user table
// would be a NEW statement against a table the platform's drift gate (db/module-statements.txt) pins
// statement by statement. The two reads cannot disagree in a way that matters: the second is one
// atomic row read, so it sees the flag and the version of one committed write together, and a user
// deleted between the reads reads as a not-suspended, version-0 user for an instant, which is what a
// deleted user's row (removed by the cascade) would read as anyway.

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// suspensionState is the wire shape of the read route. The two timestamps are pointers so an absent
// value serializes as an explicit JSON null (never an omitted key): a poller indexes the object by
// name unconditionally, as it does the health snapshot.
type suspensionState struct {
	UserID       string  `json:"user_id"`
	Suspended    bool    `json:"suspended"`
	SuspendedAt  *string `json:"suspended_at"`
	ReinstatedAt *string `json:"reinstated_at"`
	Version      int64   `json:"version"`
}

// suspensionReadSQL reads the one row, or none. One statement = one snapshot, so the flag, both
// timestamps and the version always come from the same committed write.
const suspensionReadSQL = `SELECT suspended, suspended_at, reinstated_at, version
   FROM send_byo.user_suspensions WHERE user_id = $1`

// handleUserSuspensionGet: GET /v1/admin/users/{id}/suspension.
func (s *Server) handleUserSuspensionGet(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	if !isUUID(userID) {
		s.fail(w, r, newErr(codeNotFound, "user_id_malformed"))
		return
	}

	ctx, cancel := dbCtx(r.Context())
	defer cancel()

	exists, uerr := userExists(ctx, s.db, userID)
	if uerr != nil {
		s.fail(w, r, newErr(codeInternalError, "user_lookup_error: "+uerr.Error()))
		return
	}
	if !exists {
		s.fail(w, r, newErr(codeNotFound, "user_not_found:"+userID))
		return
	}

	// Row absent = never suspended = the zero value below (suspended false, no timestamps, version 0).
	out := suspensionState{UserID: userID}
	var suspendedAt, reinstatedAt *time.Time
	err := s.db.QueryRow(ctx, suspensionReadSQL, userID).Scan(&out.Suspended, &suspendedAt, &reinstatedAt, &out.Version)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.fail(w, r, newErr(codeInternalError, "suspension_read_error: "+err.Error()))
		return
	}
	out.SuspendedAt = rfc3339NanoUTC(suspendedAt)
	out.ReinstatedAt = rfc3339NanoUTC(reinstatedAt)
	writeJSON(w, http.StatusOK, out)
}

// rfc3339NanoUTC renders a nullable timestamp as RFC 3339 with nanosecond precision in UTC, or nil.
// Postgres stores microseconds, so the fractional part has at most six digits; Go's RFC3339Nano drops
// trailing zeros, which is still a valid RFC 3339 timestamp.
func rfc3339NanoUTC(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339Nano)
	return &s
}
