package platformapi

// provider_text.go — Phase 7 §1.5: the single named accessor for jobs.last_smtp_response, the
// slug-free column serving paths P1 (GET /v1/report?job_id=), P2 (GET /v1/report?campaign_id=
// results[]), and P5 (notification bodies, §3). §1.5's closure argument is mechanical, not a
// paragraph: `grep -rn "last_smtp_response" services/brain/internal/` must return only the two
// write sites (dispatch/outcome.go) and reads INSIDE THIS FILE — any other reader in platformapi
// is a bypass by definition, enforced permanently by tests/harness/checks/user_output_scrub.sh
// (§1.6), not merely by this comment.
//
// accounts.credential_verify_response (P3/P4, Phase 3 §2.4) is NOT read here — that column has
// exactly one reader already (account_store.go's accountSelectCols), predates this phase, and was
// scrubbed BEFORE STORAGE (never concatenated with a slug, so it has no equivalent of §1.4's gap)
// — there is nothing to consolidate. This file's accessor covers only the two NEW paths §1
// introduces.

import "time"

// providerFailureDetail is what P1/P2 are allowed to know about a job's most recent failure: the
// scrubbed provider text, the error_class enum's own text value (a locked, additive-only Rule-5
// contract value — not an internal reason slug; adminapi's health endpoint already exposes the
// identical enum text on its own operator-facing surface), and the timestamp a TERMINAL
// failure/bounce completed at. NEVER last_error_message, NEVER error_reason — those stay
// internal-only (§1.4).
type providerFailureDetail struct {
	SMTPResponse *string
	ErrorClass   *string
	FailedAt     *time.Time
}

// readProviderFailureDetail is the ONE function in this package that may read
// jobs.last_smtp_response. Takes the already-scanned raw values (the caller's own SELECT already
// has the row open) rather than issuing a second query — §1.5's accessor requirement is about
// WHO reads the column, not about query count, and a second round trip per report would be a
// needless N+1 for the campaign-item list path (P2).
//
// last_smtp_response/last_error_class are STICKY BY DESIGN, same as last_error_message
// (job_events_error_context_test.go's own documented behavior: "last error this job saw" is a
// running fact that a later success does NOT clear). That stickiness is exactly why a SENT job
// must suppress all three fields here: a job that failed once, retried, and succeeded would
// otherwise show a stale failure reason on a job the user just confirmed sent — misleading, not
// merely untidy. Every OTHER state (queued/assigned/sending mid-retry, or a genuinely terminal
// failed/bounced) surfaces the columns whenever they are non-nil; a job still cycling through
// retries benefits from "here is why the last attempt failed" exactly as much as a terminal one
// does. failed_at needs no separate state check beyond the sent exclusion: completed_at is NULL
// for every non-terminal state by the schema's own jobs_completed_at_valid_check, so it is already
// nil on queued/assigned/sending without this function doing anything extra — the only value that
// needs actively suppressing is a SENT job's completed_at, which is a real timestamp but the
// wrong one (a success time, not a failure time).
func readProviderFailureDetail(state string, smtpResponse, errorClass *string, completedAt *time.Time) providerFailureDetail {
	var d providerFailureDetail
	if state == "sent" {
		return d
	}
	d.SMTPResponse = smtpResponse
	d.ErrorClass = errorClass
	if state == "failed" || state == "bounced" {
		d.FailedAt = completedAt
	}
	return d
}
