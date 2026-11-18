package platformapi

// run_causes.go — Phase 3 close-out C2: the FOUR causes of a `run` 409, named, and ONE predicate that both
// `run` (store.go's createJob) and `quote` (quote_feasibility.go) read, so the two can never disagree.
//
// THE DEFECT. `run` refuses for exactly four reasons and answered one opaque 409 for all of them:
//
//	suspended         the user is suspended (an operator or the subscription lifecycle did it)
//	campaign_active   ANOTHER campaign of this user still has a non-terminal job (one active campaign per user)
//	campaign_cap      THIS campaign already holds max_campaign_size jobs (any state)
//	queue_cap         the user's live queue (queued + assigned + sending) already holds max_queued_jobs
//
// A caller could not tell "stop" (suspended) from "wait" (campaign_active, queue_cap) from "split the
// campaign" (campaign_cap). And `quote` predicted two of them wrongly: it never read suspension and it
// computed max_campaign_size but never used it (a 60,000-recipient quote was feasible:true and the 50,001st
// run was 409), and it judged the queue cap by the request's size alone, ignoring the jobs already queued.
//
// WHAT HOLDS NOW.
//   - `run`'s 409 keeps its status (409), its code (`conflict`) and its message (the generic one) — an older
//     caller sees what it always saw — and adds `details[]`: ONE entry per cause that applies (not first-match:
//     two can hold at once), each `{"field":<cause>,"issue":"conflict","code":<cause>}` plus, for the two
//     caps, `current` and `limit` (the numbers are the user's own counts and their own resolved limits).
//   - `quote` answers from the SAME facts through the SAME function (runCauseFacts.causes): feasible:false
//     with a reason per cause that applies; `caps` gains `campaign_size_cap` (additively; the three existing
//     cap names are unchanged); an optional `campaign_id` on the request names the campaign the recipients
//     will join (omitted = a NEW campaign, the only case in which another active campaign blocks it).
//
// The one thing quote cannot know is the future: a campaign that finishes between quote and run, or another
// request that fills the queue. It answers for THIS moment, as everything else on `quote` does.

import (
	"context"
	"strings"
)

// The closed cause vocabulary of a `run` 409. Additive-only (Rule 5).
const (
	runCauseSuspended      = "suspended"
	runCauseCampaignActive = "campaign_active"
	runCauseCampaignCap    = "campaign_cap"
	runCauseQueueCap       = "queue_cap"
)

// runCauseOrder is the order the entries appear in details[] and in quote's reasons[].
var runCauseOrder = []string{runCauseSuspended, runCauseCampaignActive, runCauseCampaignCap, runCauseQueueCap}

// runCauseDetail builds one details[] entry. field is the cause too (the same convention the operator
// surface's suspension refusal uses: field = the state, issue = conflict) and `code` repeats it as the closed
// code a client switches on; current/limit are set for the two caps only.
func runCauseDetail(code string, current, limit *int) fieldDetail {
	return fieldDetail{Field: code, Issue: issueConflict, Code: code, Current: current, Limit: limit}
}

// runConflict is the 409 `run` answers with: the unchanged generic envelope plus one details[] entry per
// cause. The internal reason names the causes for the log (never serialized).
func runConflict(causes []fieldDetail) *apiError {
	codes := make([]string, len(causes))
	for i, c := range causes {
		codes[i] = c.Code
	}
	return newErr(codeConflict, "run_refused:"+strings.Join(codes, ",")).withDetails(causes...)
}

// runCauseFacts is everything the four causes are decided from, read through ONE function so run (inside its
// advisory-locked transaction) and quote (read-only, no lock) read identical facts.
type runCauseFacts struct {
	Suspended           bool
	OtherCampaignActive bool // another campaign of this user has a non-terminal job (any, when the campaign is new)
	CampaignSize        int  // jobs already in the target campaign, any state (0 for a new campaign)
	QueuedJobs          int  // the user's non-terminal jobs, all campaigns
}

// readRunCauseFacts reads the facts for userID. campaignID == "" means "a campaign that has no jobs yet" (quote
// without a campaign_id, and run for a job item that does not exist).
func readRunCauseFacts(ctx context.Context, q pgxDB, userID, campaignID string) (runCauseFacts, error) {
	var f runCauseFacts
	if err := q.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM send_byo.user_suspensions WHERE user_id=$1 AND suspended=TRUE)`, userID).
		Scan(&f.Suspended); err != nil {
		return f, err
	}
	if campaignID == "" {
		if err := q.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM send_byo.jobs WHERE user_id = $1 AND state IN ('queued','assigned','sending'))`,
			userID).Scan(&f.OtherCampaignActive); err != nil {
			return f, err
		}
	} else {
		if err := q.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM send_byo.jobs
			                WHERE user_id = $1 AND campaign_id <> $2 AND state IN ('queued','assigned','sending'))`,
			userID, campaignID).Scan(&f.OtherCampaignActive); err != nil {
			return f, err
		}
		if err := q.QueryRow(ctx,
			`SELECT count(*) FROM send_byo.jobs WHERE campaign_id = $1`, campaignID).Scan(&f.CampaignSize); err != nil {
			return f, err
		}
	}
	if err := q.QueryRow(ctx,
		`SELECT count(*) FROM send_byo.jobs WHERE user_id = $1 AND state IN ('queued','assigned','sending')`,
		userID).Scan(&f.QueuedJobs); err != nil {
		return f, err
	}
	return f, nil
}

// causes returns the details[] entries for the causes that apply when n NEW jobs are added to the campaign
// (run adds one; quote asks for recipient_count). The k-th of n runs is refused by a cap exactly when the
// count BEFORE it already equals the cap, so n jobs all fit iff current + n <= cap — the comparison below,
// which for n == 1 is run's own `current >= cap`.
func (f runCauseFacts) causes(n, maxCampaignSize, maxQueuedJobs int) []fieldDetail {
	var out []fieldDetail
	if f.Suspended {
		out = append(out, runCauseDetail(runCauseSuspended, nil, nil))
	}
	if f.OtherCampaignActive {
		out = append(out, runCauseDetail(runCauseCampaignActive, nil, nil))
	}
	if f.CampaignSize+n > maxCampaignSize {
		cur, lim := f.CampaignSize, maxCampaignSize
		out = append(out, runCauseDetail(runCauseCampaignCap, &cur, &lim))
	}
	if f.QueuedJobs+n > maxQueuedJobs {
		cur, lim := f.QueuedJobs, maxQueuedJobs
		out = append(out, runCauseDetail(runCauseQueueCap, &cur, &lim))
	}
	return out
}
