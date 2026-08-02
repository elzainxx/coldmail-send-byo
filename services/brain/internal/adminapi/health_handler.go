package adminapi

// health_handler.go — Phase 9.5 B3 (§3): GET /v1/admin/health. Operator-key auth (Domain B, the
// same requireAuth gate every other /v1/admin/* route uses), read-only, safe to poll. The response
// shape is exactly §3.2's jsonc example; active_conditions (§4, B4) is populated by health_alerts.go
// and appended here so the two chunks compose without re-plumbing this handler.

import (
	"net/http"
)

// healthResponse is the wire shape (§3.2), field-for-field.
type healthResponse struct {
	GeneratedAt string `json:"generated_at"`
	WindowSec   int    `json:"window_sec"`

	Dispatch struct {
		DispatchPaused  bool    `json:"dispatch_paused"`
		TicksPerMin     float64 `json:"ticks_per_min"`
		SendsLastWindow int     `json:"sends_last_window"`
		SendRatePerMin  float64 `json:"send_rate_per_min"`
	} `json:"dispatch"`

	Queue struct {
		Queued             int `json:"queued"`
		Assigned           int `json:"assigned"`
		Sending            int `json:"sending"`
		Sent               int `json:"sent"`
		Failed             int `json:"failed"`
		Bounced            int `json:"bounced"`
		WaitingForCapacity int `json:"waiting_for_capacity"`
	} `json:"queue"`

	Boxes struct {
		Active                      int  `json:"active"`
		Stale                       int  `json:"stale"`
		Disabled                    int  `json:"disabled"`
		OldestActiveHeartbeatAgeSec *int `json:"oldest_active_heartbeat_age_sec,omitempty"`
	} `json:"boxes"`

	Accounts struct {
		Healthy      int `json:"healthy"`
		Paused       int `json:"paused"`
		Quarantined  int `json:"quarantined"`
		Disabled     int `json:"disabled"`
		AtDailyLimit int `json:"at_daily_limit"`
	} `json:"accounts"`

	Users []userHeadroomJSON `json:"users"`

	ErrorsLastWindow map[string]int `json:"errors_last_window"`

	Shadow struct {
		AutoquarantineMode   string               `json:"autoquarantine_mode"`
		WouldQuarantineCount int                  `json:"would_quarantine_count"`
		Recent               []shadowDecisionJSON `json:"recent"`
	} `json:"shadow"`

	ActiveConditions []activeConditionJSON `json:"active_conditions"`
}

// userHeadroomJSON is the G3 REAL REWRITE (M3-P2-OD-12) of the pre-G2 per-pool wire shape,
// now per-user — see health_store.go's queryUserHeadroom for the rationale. Wire-contract note:
// this is a documented, pre-announced shape change (api-contract.md's own G2-stub row already
// said "a G3 account-set-aware replacement is expected, at which point this row will change
// again"), not an undisclosed break — the field is also renamed pools->users in the same commit
// so a client cannot mistake the new per-user rows for the old per-pool ones.
type userHeadroomJSON struct {
	UserID           string `json:"user_id"`
	EligibleAccounts int    `json:"eligible_accounts"`
	Headroom         string `json:"headroom"`
}

// shadowDecisionJSON's Trigger field is Module 3 Phase 5 §8.2's vocabulary (auth_failure |
// consecutive_failures | failure_rate) — the score/threshold pair module 2 reported here is gone
// (§4: the score never independently quarantines, so a shadow decision has nothing to report there).
type shadowDecisionJSON struct {
	AccountID string `json:"account_id"`
	Trigger   string `json:"trigger"`
	At        string `json:"at"`
}

// errorsLastWindowClasses is the fixed §1.1 error_class vocabulary — the response always includes
// every class with a 0 default, never omitting a key just because it didn't fire in the window
// (stable shape for a poller that indexes the map by class name).
var errorsLastWindowClasses = []string{
	"bounce_permanent", "transport_4xx", "auth_failure", "throttle", "timeout", "unknown",
}

// handleHealth: GET /v1/admin/health (§3.1). Operator auth only; every field derives from
// COUNT/GROUP BY over existing tables plus the live dispatch_enabled/autoquarantine_mode config —
// no schema change, no new persisted state.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r.Context())
	defer cancel()

	windowSec, err := readConfigInt(ctx, s.db, "health_metrics_window_sec")
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "health_window_read_failed: "+err.Error()))
		return
	}
	dispatchEnabled, err := readConfigBool(ctx, s.db, "dispatch_enabled")
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "health_dispatch_enabled_read_failed: "+err.Error()))
		return
	}
	autoquarantineMode, err := readConfigString(ctx, s.db, "autoquarantine_mode")
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "health_autoquarantine_mode_read_failed: "+err.Error()))
		return
	}

	snap, err := buildHealthSnapshot(ctx, s.db, windowSec, dispatchEnabled, autoquarantineMode)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "health_snapshot_failed: "+err.Error()))
		return
	}

	// active_conditions (§4, B4): pool_exhausted / account_quarantined (enforce) / box_death.
	conditions, err := buildActiveConditions(ctx, s.db, snap)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "health_conditions_failed: "+err.Error()))
		return
	}

	// §4.3 the optional webhook: fire on each NEW rising edge, never on every poll (dedup). Reads
	// alert_webhook_url fresh (no redeploy needed for an operator to set/unset it) and fans out in
	// its own goroutine so a slow/unreachable operator endpoint can never delay this response —
	// this GET is otherwise entirely read-only.
	webhookURL, err := readConfigString(ctx, s.db, "alert_webhook_url")
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "health_webhook_url_read_failed: "+err.Error()))
		return
	}
	go fireWebhooks(webhookURL, conditions)

	resp := healthResponse{
		GeneratedAt: snap.GeneratedAt.Format("2006-01-02T15:04:05Z"),
		WindowSec:   snap.WindowSec,
	}
	resp.Dispatch.DispatchPaused = snap.DispatchPaused
	resp.Dispatch.TicksPerMin = snap.TicksPerMin
	resp.Dispatch.SendsLastWindow = snap.SendsLastWindow
	resp.Dispatch.SendRatePerMin = snap.SendRatePerMin

	resp.Queue.Queued = snap.Queue.Queued
	resp.Queue.Assigned = snap.Queue.Assigned
	resp.Queue.Sending = snap.Queue.Sending
	resp.Queue.Sent = snap.Queue.Sent
	resp.Queue.Failed = snap.Queue.Failed
	resp.Queue.Bounced = snap.Queue.Bounced
	resp.Queue.WaitingForCapacity = snap.Queue.WaitingForCapacity

	resp.Boxes.Active = snap.Boxes.Active
	resp.Boxes.Stale = snap.Boxes.Stale
	resp.Boxes.Disabled = snap.Boxes.Disabled
	resp.Boxes.OldestActiveHeartbeatAgeSec = snap.Boxes.OldestActiveHeartbeatAgeSec

	resp.Accounts.Healthy = snap.Accounts.Healthy
	resp.Accounts.Paused = snap.Accounts.Paused
	resp.Accounts.Quarantined = snap.Accounts.Quarantined
	resp.Accounts.Disabled = snap.Accounts.Disabled
	resp.Accounts.AtDailyLimit = snap.Accounts.AtDailyLimit

	resp.Users = make([]userHeadroomJSON, 0, len(snap.Users))
	for _, u := range snap.Users {
		resp.Users = append(resp.Users, userHeadroomJSON{
			UserID: u.UserID, EligibleAccounts: u.EligibleAccounts, Headroom: u.Headroom,
		})
	}

	resp.ErrorsLastWindow = make(map[string]int, len(errorsLastWindowClasses))
	for _, class := range errorsLastWindowClasses {
		resp.ErrorsLastWindow[class] = snap.ErrorsLastWindow[class] // 0 default for a class absent from the map
	}

	resp.Shadow.AutoquarantineMode = snap.Shadow.AutoquarantineMode
	resp.Shadow.WouldQuarantineCount = snap.Shadow.WouldQuarantineCount
	resp.Shadow.Recent = make([]shadowDecisionJSON, 0, len(snap.Shadow.Recent))
	for _, d := range snap.Shadow.Recent {
		resp.Shadow.Recent = append(resp.Shadow.Recent, shadowDecisionJSON{
			AccountID: d.AccountID, Trigger: d.Trigger,
			At: d.At.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}

	resp.ActiveConditions = conditions
	writeJSON(w, http.StatusOK, resp)
}
