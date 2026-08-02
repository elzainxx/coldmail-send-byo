package platformapi

import (
	"context"
	"net/http"
)

// Account lifecycle transitions, re-scoped from adminapi (§1.2/§1.4/§1.5): disable/enable/pause/
// unpause only. quarantine/unquarantine are DROPPED from this user surface entirely (§1.2) — the
// system is itself an autonomous writer of that flag, and a user-facing unquarantine needs the
// rate limit + test-verified bypass Phase 5 owns; exposing a plain re-scope here would ship the
// loop the limit exists to prevent.

type lifecycleReq struct {
	Reason *string `json:"reason"`
}

type lifecycleSpec struct {
	action       string
	setClause    string
	precondition func(accountFlags) *apiError
	apply        func(accountFlags) accountFlags
}

func lifecycleRefusal(reason, message, flag string) *apiError {
	return newErr(codeUnprocessable, reason).
		withMessage(message).
		withDetails(fieldDetail{Field: flag, Issue: issueConflict})
}

var (
	specDisable = lifecycleSpec{
		action:    "disable",
		setClause: "disabled = TRUE",
		precondition: func(f accountFlags) *apiError {
			if f.Disabled {
				return lifecycleRefusal("already_in_state:disabled", "This account is already disabled.", "disabled")
			}
			return nil
		},
		apply: func(f accountFlags) accountFlags { f.Disabled = true; return f },
	}
	specEnable = lifecycleSpec{
		action:    "enable",
		setClause: "disabled = FALSE",
		precondition: func(f accountFlags) *apiError {
			if !f.Disabled {
				return lifecycleRefusal("not_in_state:disabled", "This account is not disabled.", "disabled")
			}
			return nil
		},
		apply: func(f accountFlags) accountFlags { f.Disabled = false; return f },
	}
	specPause = lifecycleSpec{
		action:    "pause",
		setClause: "paused = TRUE, pause_reason = 'manual'",
		precondition: func(f accountFlags) *apiError {
			if f.Paused {
				return lifecycleRefusal("already_in_state:paused", "This account is already paused.", "paused")
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
	// unpause carries §1.4's ONE piece of user-safety logic VERBATIM: it refuses to clear a pause
	// whose reason is not 'manual', so a user cannot lift a throttle/daily_limit pause the pacing
	// logic owns. This precondition is why unpause is safe to expose to users at all.
	specUnpause = lifecycleSpec{
		action:    "unpause",
		setClause: "paused = FALSE, pause_reason = NULL",
		precondition: func(f accountFlags) *apiError {
			if !f.Paused {
				return lifecycleRefusal("not_in_state:paused", "This account is not paused.", "paused")
			}
			if f.PauseReason == nil || *f.PauseReason != "manual" {
				return lifecycleRefusal("not_manual_pause",
					"This pause was not set manually and clears automatically; only a manual pause can be lifted here.",
					"pause_reason")
			}
			return nil
		},
		apply: func(f accountFlags) accountFlags { f.Paused = false; f.PauseReason = nil; return f },
	}
)

// doLifecycleTransition is the shared engine (§1.5: one change point, not four). TENANT-SCOPED —
// readFlags is the SAME tenant-scoped read every other verb uses, so a cross-tenant id is
// not-found before any precondition check or write.
func (s *Server) doLifecycleTransition(w http.ResponseWriter, r *http.Request, spec lifecycleSpec) {
	id := r.PathValue("id")
	userID := r.URL.Query().Get("user_id")
	if e := requireTenantSelector(userID); e != nil {
		s.fail(w, r, e)
		return
	}
	if !isUUID(id) {
		s.fail(w, r, newErr(codeNotFound, "account_id_malformed"))
		return
	}
	var req lifecycleReq
	if _, derr := decodeStrictPresence(r, &req); derr != nil {
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

	from, err := readFlags(ctx, tx, id, userID)
	if err == errAccountNotFound {
		s.fail(w, r, newErr(codeNotFound, "account_not_found:"+id))
		return
	}
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "flags_read_error: "+err.Error()))
		return
	}

	if perr := spec.precondition(from); perr != nil {
		s.fail(w, r, perr)
		return
	}

	if _, err := tx.Exec(ctx,
		"UPDATE byosmtp.accounts SET "+spec.setClause+", updated_at = NOW() WHERE id=$1 AND user_id=$2 AND deleted_at IS NULL",
		id, userID); err != nil {
		s.fail(w, r, s.translateAccountWrite(err))
		return
	}

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

	acc, err := getAccount(ctx, s.db, id, userID)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "account_reread_error: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, acc)
}

func (s *Server) handleAccountDisable(w http.ResponseWriter, r *http.Request) {
	s.doLifecycleTransition(w, r, specDisable)
}
func (s *Server) handleAccountEnable(w http.ResponseWriter, r *http.Request) {
	s.doLifecycleTransition(w, r, specEnable)
}
func (s *Server) handleAccountPause(w http.ResponseWriter, r *http.Request) {
	s.doLifecycleTransition(w, r, specPause)
}
func (s *Server) handleAccountUnpause(w http.ResponseWriter, r *http.Request) {
	s.doLifecycleTransition(w, r, specUnpause)
}
