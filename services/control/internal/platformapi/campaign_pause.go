package platformapi

// campaign_pause.go — POST /v1/pause and POST /v1/resume (Phase 4 C1, owner ruling: modules 2 and 3 each get PER-CAMPAIGN
// pause and resume, so the platform can hold one running send job and continue it later). Before this the only way to
// stop a campaign was cancel, which is final. The request, the answers and the report field are written to be IDENTICAL in
// name and meaning to module 2's, which another build is adding at the same time, so the platform's one adapter treats
// both senders the same.
//
// THE CONTRACT (additive; Domain A auth; same envelope and error codes as the sibling routes):
//
//	POST /v1/pause   {"campaign_id": <uuid>, "user_id": <uuid>}
//	POST /v1/resume  {"campaign_id": <uuid>, "user_id": <uuid>}
//	200  {"campaign_id": …, "state": "paused" | "running" | "cancelled" | "finished" | "unknown", "paused": <bool>}
//
//   - Request shape, authentication, validation and the tenant selector are cancel's (user_id is the TENANT, every
//     statement is scoped `campaign_id = $1 AND user_id = $2`). A campaign of another tenant, an unknown campaign id and
//     a campaign with nothing in it are INDISTINGUISHABLE: all answer 200 state "unknown", paused false, and change
//     nothing — the campaign report's and cancel's own rule (a campaign id is not a resource this module allocates, so
//     "no items under this id for this tenant" is the honest answer and never an existence signal).
//   - `state` is the campaign's state AFTER the call (campaignFacts.state, one definition shared with the report):
//     paused     a campaign_pauses row exists and something is still unfinished: no queued item of it is claimed
//     running    not paused and something is still unfinished (the common case)
//     cancelled  POST /v1/cancel stopped it and nothing is left that pause or resume could act on
//     finished   every item is final (sent/failed/bounced) and none was cancelled
//     unknown    no item of this campaign exists for this tenant
//     `paused` is state == "paused", spelled as the boolean the campaign report carries too.
//   - IDEMPOTENT. pause of a paused campaign, resume of a campaign that is not paused: 200 with the current state, and
//     NOTHING changes — no row, no event. An actual transition writes exactly ONE campaign_events row; a no-op writes
//     none (two pause calls racing each other still write one: the second one's insert conflicts and it writes nothing).
//   - CANCELLED, FINISHED and UNKNOWN are no-ops for BOTH verbs, so neither verb can ever un-cancel anything: pause and
//     resume never touch an item's state, flag or row in send_byo.jobs at all. A pause of a cancelled campaign answers
//     200 "cancelled" and stores nothing; so does a resume.
//   - Never gated by the dispatch rung or by the user's suspension (cancel's rule, and for the same reason): recording the
//     tenant's instruction about its own campaign is not sending. A resume of a campaign whose user is SUSPENDED answers
//     200 "running" and the user stays suspended: claiming needs BOTH, see below.
//
// WHAT A PAUSE DOES (and what it deliberately does not). The campaign's row in send_byo.campaign_pauses is the whole
// state. Every statement that can CLAIM a job (dispatch.claimQuery, claimForUserQuery) and the ring-membership snapshot
// (snapshotActiveUsers) skip a queued job whose campaign has that row, as the one shared dispatch.notPausedFragment, in
// the very statement that claims — so no claim can slip past a pause that committed before it began. Nothing else
// changes: items keep their states (queued stays queued; waiting_for_capacity is still an aggregate of queued, nothing
// new is invented), items already claimed finish and are reported once, an item submitted by a later /v1/run for a
// paused campaign is accepted and held with the rest, and resume simply deletes the row so the same queued items are
// claimable again and sending continues where it stopped. Every item is sent exactly once overall: nothing is
// re-created, re-queued or re-sent by either verb.
//
// SUSPENSION (the operator's per-user control, send_byo.user_suspensions) AND PAUSE ARE TWO INDEPENDENT TABLES, ANDed in the
// claim. Either blocks claiming. Resuming a campaign never touches user_suspensions, so it can never override a
// suspension; lifting a suspension never touches campaign_pauses, so it can never resume a campaign.
//
// THE EXPIRY CLOCK. A campaign's expiry window (campaign_expiry_sec, frozen at its first job) must not run out while the
// campaign is HELD, or a pause would eventually turn its queued items into `failed`. The campaign-expiry sweep therefore
// skips a paused campaign (dispatch/campaign_expiry.go), and resume credits the paused interval back to
// campaigns.expires_at, exactly as the suspension reinstate does. (The exhaustion sweep skips a paused campaign's items
// for the same reason: dispatch/exhaustion.go.) If a suspension overlaps a pause, both credits apply and the overlap is
// credited twice — the campaign's window ends a little LATER than strictly necessary, never earlier.
//
// RACES. Two overlapping calls on one campaign serialize on the (campaign_id, user_id) primary key of campaign_pauses:
// pause is INSERT … ON CONFLICT DO NOTHING (the loser inserts nothing and writes no event), resume is DELETE … RETURNING (the
// loser deletes nothing and writes no event). A claim statement that began before a pause committed may still assign one
// item: that item is in flight and finishes, as the contract says. Both halves are proven against real Postgres
// (platformapi/campaign_pause_test.go, dispatch/campaign_pause_test.go), the second with a forced interleave.
//
// NO LOCKS OF ITS OWN ON JOB OR ACCOUNT ROWS. This file only READS send_byo.jobs (plain snapshot reads, no FOR UPDATE) and
// writes the two new tables, so it adds no edge to the account → job lock order (CLAUDE.md Rule 9).

import (
	"context"
	"net/http"
	"time"
)

// The closed vocabulary of `state` in a pause/resume answer, and of the campaign report's `paused` (== state is paused).
const (
	campaignStatePaused    = "paused"
	campaignStateRunning   = "running"
	campaignStateCancelled = "cancelled"
	campaignStateFinished  = "finished"
	campaignStateUnknown   = "unknown"
)

// The closed vocabulary of send_byo.campaign_events.action (the table's CHECK constraint states the same set).
const (
	campaignEventPaused  = "paused"
	campaignEventResumed = "resumed"
)

// campaignPauseReq is the POST /v1/pause and POST /v1/resume body — cancel's, field for field.
type campaignPauseReq struct {
	CampaignID string `json:"campaign_id"` // the campaign to hold or release
	UserID     string `json:"user_id"`     // the owning tenant, REQUIRED
}

// campaignPauseResult is the 200 body of both routes.
type campaignPauseResult struct {
	CampaignID string `json:"campaign_id"`
	State      string `json:"state"`
	Paused     bool   `json:"paused"`
}

// campaignFacts is everything campaignFacts.state needs, read by ONE statement (one snapshot) so the five numbers describe
// a single consistent moment.
type campaignFacts struct {
	total       int  // every item of the campaign
	nonFinal    int  // queued + assigned + sending
	live        int  // non-final AND not flagged by cancel: what a pause could still hold or a claim could still take
	cancelMarks int  // items in state cancelled OR flagged cancel_requested_at: the footprint of a cancel
	hasRow      bool // a send_byo.campaign_pauses row exists for (campaign, tenant)
}

// state is the one definition of a campaign's pause state, shared by pause, resume and the campaign report. The order of
// the cases is the meaning: nothing there -> unknown; stopped by cancel with nothing left to act on -> cancelled; nothing
// left unfinished -> finished; otherwise the stored flag decides paused or running.
//
// `cancelled` needs `live == 0`, not merely a mark: a campaign that was cancelled and then received NEW items (cancel's
// header: the platform that cancelled is the one that decides not to submit more, but a later /v1/run is accepted as
// usual) has live work again, and that work can be held like any other.
func (f campaignFacts) state() string {
	switch {
	case f.total == 0:
		return campaignStateUnknown
	case f.cancelMarks > 0 && f.live == 0:
		return campaignStateCancelled
	case f.nonFinal == 0:
		return campaignStateFinished
	case f.hasRow:
		return campaignStatePaused
	default:
		return campaignStateRunning
	}
}

// campaignFactsSQL reads the facts in one statement. The aggregate always yields exactly one row (all zeros for a campaign
// with nothing in it), so "unknown" needs no separate not-found branch, and a foreign tenant's campaign reads as the same
// zeros an unknown id does.
const campaignFactsSQL = `
SELECT COUNT(*),
       COUNT(*) FILTER (WHERE state IN ('queued', 'assigned', 'sending')),
       COUNT(*) FILTER (WHERE state IN ('queued', 'assigned', 'sending') AND cancel_requested_at IS NULL),
       COUNT(*) FILTER (WHERE state = 'cancelled' OR cancel_requested_at IS NOT NULL),
       EXISTS (SELECT 1 FROM send_byo.campaign_pauses WHERE campaign_id = $1 AND user_id = $2)
  FROM send_byo.jobs
 WHERE campaign_id = $1 AND user_id = $2`

// campaignPauseInsertSQL stores the pause. ON CONFLICT DO NOTHING is what makes two racing pauses write ONE row and, in the
// caller, ONE event: the loser's insert waits for the winner, finds the row, inserts nothing, and RowsAffected says so.
const campaignPauseInsertSQL = `
INSERT INTO send_byo.campaign_pauses (campaign_id, user_id) VALUES ($1, $2)
ON CONFLICT (campaign_id, user_id) DO NOTHING`

// campaignResumeDeleteSQL releases the pause and hands back WHEN it began (the interval the expiry clock is credited).
// A racing second resume deletes nothing and gets no row back.
const campaignResumeDeleteSQL = `
DELETE FROM send_byo.campaign_pauses
 WHERE campaign_id = $1 AND user_id = $2
RETURNING paused_at`

// campaignExpiryCreditSQL pushes the campaign's expiry FORWARD by the interval it was held and accounts for it in
// paused_total_sec — the self-referential credit the suspension reinstate gives (adminapi/user_suspend.go,
// reinstateCampaignsSQL). $3 is the paused_at the delete returned. An already-expired campaign is left alone.
const campaignExpiryCreditSQL = `
UPDATE send_byo.campaigns
   SET expires_at = expires_at + (NOW() - $3::timestamptz),
       paused_total_sec = paused_total_sec + EXTRACT(EPOCH FROM (NOW() - $3::timestamptz))::bigint,
       updated_at = NOW()
 WHERE id = $1 AND user_id = $2 AND expired_at IS NULL`

// campaignEventInsertSQL writes the audit row of one ACTUAL transition. The actor is `system`: the platform's service
// token made the call, which is what cancel's own job events record as well.
const campaignEventInsertSQL = `
INSERT INTO send_byo.campaign_events (campaign_id, user_id, action, actor, request_id)
VALUES ($1, $2, $3, 'system', NULLIF($4::text, ''))`

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	s.handleCampaignPauseResume(w, r, true)
}
func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	s.handleCampaignPauseResume(w, r, false)
}

// handleCampaignPauseResume validates the request exactly as handleCancel does (same fields, same required/malformed
// vocabulary, same order) and runs the transition.
func (s *Server) handleCampaignPauseResume(w http.ResponseWriter, r *http.Request, pause bool) {
	var req campaignPauseReq
	if e := decodeStrict(r.Body, &req); e != nil {
		s.fail(w, r, e)
		return
	}
	if req.CampaignID == "" {
		s.fail(w, r, newErr(codeValidationFailed, "campaign_id_required").
			withDetails(fieldDetail{Field: "campaign_id", Issue: issueRequired}))
		return
	}
	if !uuidRe.MatchString(req.CampaignID) {
		s.fail(w, r, newErr(codeValidationFailed, "campaign_id_malformed").
			withDetails(fieldDetail{Field: "campaign_id", Issue: issueMalformed}))
		return
	}
	if req.UserID == "" {
		s.fail(w, r, newErr(codeValidationFailed, "user_id_required").
			withDetails(fieldDetail{Field: "user_id", Issue: issueRequired}))
		return
	}
	if !uuidRe.MatchString(req.UserID) {
		s.fail(w, r, newErr(codeValidationFailed, "user_id_malformed").
			withDetails(fieldDetail{Field: "user_id", Issue: issueMalformed}))
		return
	}

	ctx, cancel := dbCtx(r.Context())
	defer cancel()

	res, err := s.setCampaignPaused(ctx, req.CampaignID, req.UserID, pause, requestIDFrom(r.Context()))
	if err != nil {
		reason := "campaign_resume_failed: "
		if pause {
			reason = "campaign_pause_failed: "
		}
		s.fail(w, r, newErr(codeInternalError, reason+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// readCampaignFacts runs campaignFactsSQL against a pool or a transaction.
func readCampaignFacts(ctx context.Context, q pgxDB, campaignID, userID string) (campaignFacts, error) {
	var f campaignFacts
	err := q.QueryRow(ctx, campaignFactsSQL, campaignID, userID).
		Scan(&f.total, &f.nonFinal, &f.live, &f.cancelMarks, &f.hasRow)
	return f, err
}

// setCampaignPaused is the whole transition for one verb, in ONE transaction: read the state, and — only when the state
// says this call is a real transition — write the pause row (or delete it, crediting the expiry clock) and exactly one
// event row. Every other state is a no-op that writes nothing; the transaction then commits empty.
//
// A real pause needs state `running` (something unfinished and not yet paused); a real resume needs state `paused`. That is
// what makes cancelled, finished and unknown campaigns no-ops for both verbs and keeps a pause from ever being stored for
// nothing.
func (s *Server) setCampaignPaused(ctx context.Context, campaignID, userID string, pause bool, requestID string) (campaignPauseResult, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return campaignPauseResult{}, err
	}
	defer tx.Rollback(ctx)

	facts, err := readCampaignFacts(ctx, tx, campaignID, userID)
	if err != nil {
		return campaignPauseResult{}, err
	}
	state := facts.state()

	switch {
	case pause && state == campaignStateRunning:
		tag, err := tx.Exec(ctx, campaignPauseInsertSQL, campaignID, userID)
		if err != nil {
			return campaignPauseResult{}, err
		}
		// 0 rows: a racing pause inserted the row between our read and our insert and committed first. The campaign IS
		// paused, and that call wrote the event; this one writes none.
		if tag.RowsAffected() == 1 {
			if _, err := tx.Exec(ctx, campaignEventInsertSQL, campaignID, userID, campaignEventPaused, requestID); err != nil {
				return campaignPauseResult{}, err
			}
		}
		state = campaignStatePaused

	case !pause && state == campaignStatePaused:
		var pausedAt time.Time
		err := tx.QueryRow(ctx, campaignResumeDeleteSQL, campaignID, userID).Scan(&pausedAt)
		switch {
		case err == nil:
			if _, err := tx.Exec(ctx, campaignExpiryCreditSQL, campaignID, userID, pausedAt); err != nil {
				return campaignPauseResult{}, err
			}
			if _, err := tx.Exec(ctx, campaignEventInsertSQL, campaignID, userID, campaignEventResumed, requestID); err != nil {
				return campaignPauseResult{}, err
			}
		case isNoRows(err):
			// a racing resume deleted the row first and wrote the event; nothing to do here
		default:
			return campaignPauseResult{}, err
		}
		state = campaignStateRunning
	}

	if err := tx.Commit(ctx); err != nil {
		return campaignPauseResult{}, err
	}
	return campaignPauseResult{CampaignID: campaignID, State: state, Paused: state == campaignStatePaused}, nil
}
