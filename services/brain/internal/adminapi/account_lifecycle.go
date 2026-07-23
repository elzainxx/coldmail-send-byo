package adminapi

import (
	"context"
	"net/http"
)

// Account state transitions (§7): manual, audited, precondition-guarded. Each moves ONLY the
// intended flag(s) and writes EXACTLY ONE account_events row (actor='admin') in the same tx as
// the UPDATE. An illegal transition is rejected 422 with NO audit row written (the guard runs
// before the write). Phase 2 does NO scoring and NO automatic transitions.

type transitionReq struct {
	Reason *string `json:"reason"` // optional operator note (internal; never echoed in an error)
}

// transitionSpec describes one lifecycle endpoint: how it mutates flags, its precondition, and
// the audit action name.
type transitionSpec struct {
	action    string
	setClause string // SQL SET fragment (no args; flags are literals here)
	// precondition returns an apiError (422) if the current flags forbid the transition, else nil.
	precondition func(accountFlags) *apiError
	// apply computes the post-state flags for the audit to_flags snapshot.
	apply func(accountFlags) accountFlags
}

func alreadyInState(reason string) *apiError { return newErr(codeUnprocessable, reason) }

var (
	specQuarantine = transitionSpec{
		action:    "quarantine",
		setClause: "quarantined = TRUE",
		precondition: func(f accountFlags) *apiError {
			if f.Quarantined {
				return alreadyInState("already_in_state:quarantined")
			}
			return nil
		},
		apply: func(f accountFlags) accountFlags { f.Quarantined = true; return f },
	}
	specUnquarantine = transitionSpec{
		action:    "unquarantine",
		setClause: "quarantined = FALSE",
		precondition: func(f accountFlags) *apiError {
			if !f.Quarantined {
				return alreadyInState("not_in_state:quarantined")
			}
			return nil
		},
		apply: func(f accountFlags) accountFlags { f.Quarantined = false; return f },
	}
	specDisable = transitionSpec{
		action:    "disable",
		setClause: "disabled = TRUE",
		precondition: func(f accountFlags) *apiError {
			if f.Disabled {
				return alreadyInState("already_in_state:disabled")
			}
			return nil
		},
		apply: func(f accountFlags) accountFlags { f.Disabled = true; return f },
	}
	specEnable = transitionSpec{
		action:    "enable",
		setClause: "disabled = FALSE",
		precondition: func(f accountFlags) *apiError {
			if !f.Disabled {
				return alreadyInState("not_in_state:disabled")
			}
			return nil
		},
		apply: func(f accountFlags) accountFlags { f.Disabled = false; return f },
	}
	specPause = transitionSpec{
		action:    "pause",
		setClause: "paused = TRUE, pause_reason = 'manual'",
		precondition: func(f accountFlags) *apiError {
			if f.Paused {
				return alreadyInState("already_in_state:paused")
			}
			return nil
		},
		apply: func(f accountFlags) accountFlags {
			f.Paused = true
			m := "manual"
			f.PauseReason = &m
			return f
		},
	}
	// Unpause ONLY clears a pause whose reason is 'manual' (§7.1 guard). It refuses to clear a
	// daily_limit/throttle pause (owned by the daily-reset logic / Phase 5).
	specUnpause = transitionSpec{
		action:    "unpause",
		setClause: "paused = FALSE, pause_reason = NULL",
		precondition: func(f accountFlags) *apiError {
			if !f.Paused {
				return alreadyInState("not_in_state:paused")
			}
			if f.PauseReason == nil || *f.PauseReason != "manual" {
				return newErr(codeUnprocessable, "not_manual_pause")
			}
			return nil
		},
		apply: func(f accountFlags) accountFlags { f.Paused = false; f.PauseReason = nil; return f },
	}
)

// doTransition is the shared engine for every §7 endpoint.
func (s *Server) doTransition(w http.ResponseWriter, r *http.Request, spec transitionSpec) {
	id := r.PathValue("id")
	if !isUUID(id) {
		s.fail(w, r, newErr(codeNotFound, "account_id_malformed"))
		return
	}
	var req transitionReq
	if _, derr := decodeStrict(r, &req); derr != nil {
		s.fail(w, r, derr)
		return
	}
	if req.Reason != nil && len(*req.Reason) > 2000 {
		s.fail(w, r, newErr(codeValidationFailed, "reason_too_long").
			withDetails(fieldDetail{Field: "reason", Issue: issueMalformed}))
		return
	}

	ctx, cancel := dbCtx(r.Context())
	defer cancel()

	tx, err := s.db.Begin(ctx)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "tx_begin_error: "+err.Error()))
		return
	}
	defer tx.Rollback(context.Background())

	// Read current flags + user_id inside the tx (consistent precondition + audit snapshot).
	from, err := readFlags(ctx, tx, id)
	if err == errAccountNotFound {
		s.fail(w, r, newErr(codeNotFound, "account_not_found:"+id))
		return
	}
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "flags_read_error: "+err.Error()))
		return
	}
	var userID string
	if err := tx.QueryRow(ctx, "SELECT user_id FROM sender.accounts WHERE id=$1", id).Scan(&userID); err != nil {
		s.fail(w, r, newErr(codeInternalError, "user_id_read_error: "+err.Error()))
		return
	}

	// Precondition guard — an illegal transition writes NO row.
	if perr := spec.precondition(from); perr != nil {
		s.fail(w, r, perr)
		return
	}

	// Apply the flag mutation.
	if _, err := tx.Exec(ctx, "UPDATE sender.accounts SET "+spec.setClause+", updated_at = NOW() WHERE id=$1", id); err != nil {
		s.fail(w, r, s.translateAccountWrite(err))
		return
	}

	// Exactly one audit row: from = pre-state, to = post-state.
	fromSnap := snapshotOf(from)
	toSnap := snapshotOf(spec.apply(from))
	if err := writeAuditEvent(ctx, tx, id, userID, spec.action, &fromSnap, &toSnap, req.Reason, requestIDFrom(r.Context())); err != nil {
		s.fail(w, r, newErr(codeInternalError, "audit_write_error: "+err.Error()))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		s.fail(w, r, s.translateAccountWrite(err))
		return
	}

	acc, err := getAccount(ctx, s.db, id)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "account_reread_error: "+err.Error()))
		return
	}
	s.logOK(requestIDFrom(r.Context()), r, "account_"+spec.action+" id="+id)
	writeJSON(w, http.StatusOK, acc)
}

func (s *Server) handleAccountQuarantine(w http.ResponseWriter, r *http.Request) {
	s.doTransition(w, r, specQuarantine)
}
func (s *Server) handleAccountUnquarantine(w http.ResponseWriter, r *http.Request) {
	s.doTransition(w, r, specUnquarantine)
}
func (s *Server) handleAccountDisable(w http.ResponseWriter, r *http.Request) {
	s.doTransition(w, r, specDisable)
}
func (s *Server) handleAccountEnable(w http.ResponseWriter, r *http.Request) {
	s.doTransition(w, r, specEnable)
}
func (s *Server) handleAccountPause(w http.ResponseWriter, r *http.Request) {
	s.doTransition(w, r, specPause)
}
func (s *Server) handleAccountUnpause(w http.ResponseWriter, r *http.Request) {
	s.doTransition(w, r, specUnpause)
}
