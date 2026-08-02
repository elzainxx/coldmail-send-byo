package dispatch

// attribution_test.go — Phase 7 §2.2/T5. Real Postgres, real Dispatcher.Tick calls — no mocks
// (CLAUDE.md Rule 3). T5's own framing: assert the MAPPING is correct, not merely that
// jobs.assigned_account_id is populated — a populated-but-wrong field passes any "is it set"
// check, and a uniform expected distribution is indistinguishable from round-robin-by-accident.

import (
	"context"
	"testing"

	"github.com/byosmtp-module/brain/internal/boxproto"
)

// TestAttribution_T5_MappingMatchesIndependentSourceUnderForcedSkew — three accounts, one user, a
// FORCED skew (two accounts already at daily_limit, one open) so the correct mapping is NOT
// uniform: every dispatched job must land on the one open account. For each dispatched job,
// jobs.assigned_account_id (what platformapi's readReport/campaignItems actually read) is
// cross-checked against job_events.account_id — a column written by the SAME trigger from the
// SAME NEW.assigned_account_id value at commit time, but read through an independent path (a
// different table, a different row) rather than re-reading the same column twice.
func TestAttribution_T5_MappingMatchesIndependentSourceUnderForcedSkew(t *testing.T) {
	e := newDispatchEnv(t, "attrt5skew")
	ctx := context.Background()
	userID := e.seedUser(t, "attrt5skew@example.com")

	acctFull1 := e.seedAccountForUser(t, userID, "sec1")
	acctFull2 := e.seedAccountForUser(t, userID, "sec2")
	acctOpen := e.seedAccountForUser(t, userID, "sec3")
	e.setAccountDailyState(t, acctFull1, 10, 10, todayUTC(), false, nil) // AT limit
	e.setAccountDailyState(t, acctFull2, 10, 10, todayUTC(), false, nil) // AT limit
	e.setAccountDailyState(t, acctOpen, 1000, 0, todayUTC(), false, nil) // wide open

	const n = 5
	jobIDs := make([]string, 0, n)
	for i := 0; i < n; i++ {
		jobIDs = append(jobIDs, e.seedQueuedJobFor(t, userID, "t5-"+string(rune('a'+i))+"@example.com"))
	}

	// One job per tick, resolved to 'sent' immediately: snapshotInFlight counts
	// state IN ('assigned','sending') against concurrency_per_user_default, so a job left
	// in-flight after its tick would silently cap this loop at that ceiling well before all n
	// jobs are dispatched — resolving each one is what lets every job's OWN attribution be
	// checked, not merely the first few.
	dispatched := 0
	for dispatched < n {
		e.px.freeAllBoxes()
		_, pushed, err := e.d.Tick(ctx)
		if err != nil {
			t.Fatalf("tick: %v", err)
		}
		if pushed == 0 {
			break
		}
		pj, ok := e.px.lastPush()
		if !ok {
			t.Fatal("Tick reported a push but recordingPusher has none recorded")
		}
		e.oh.Handle(e.px.boxID, boxproto.Report{JobID: pj.JobID, Outcome: boxproto.OutcomeAccepted, ProviderMessageID: "pmid-" + pj.JobID})
		dispatched += pushed
	}
	if dispatched != n {
		t.Fatalf("dispatched %d of %d jobs — the skew fixture should leave exactly one account able to take all of them", dispatched, n)
	}

	for _, jobID := range jobIDs {
		var fromJobs, fromEvents *string
		if err := e.admin.QueryRow(ctx,
			`SELECT assigned_account_id::text FROM byosmtp.jobs WHERE id=$1`, jobID).Scan(&fromJobs); err != nil {
			t.Fatalf("read jobs.assigned_account_id: %v", err)
		}
		if err := e.admin.QueryRow(ctx,
			`SELECT account_id::text FROM byosmtp.job_events
			  WHERE job_id=$1 AND to_state='sending' ORDER BY seq DESC LIMIT 1`, jobID).Scan(&fromEvents); err != nil {
			t.Fatalf("read job_events.account_id: %v", err)
		}
		if fromJobs == nil || fromEvents == nil {
			t.Fatalf("job %s: assigned_account_id missing from one of the two sources (jobs=%v, job_events=%v)", jobID, fromJobs, fromEvents)
		}
		if *fromJobs != *fromEvents {
			t.Fatalf("job %s: jobs.assigned_account_id=%s but job_events.account_id=%s — the two sources disagree", jobID, *fromJobs, *fromEvents)
		}
		// THE MAPPING ASSERTION (not just "is it set"): under this forced skew, EVERY job must
		// have landed on the one open account — never on either account already at its limit.
		if *fromJobs != acctOpen {
			t.Fatalf("job %s assigned to %s, want the OPEN account %s (the two at-limit accounts must never be chosen)", jobID, *fromJobs, acctOpen)
		}
	}
}

// TestAttribution_T5_Control_QueuedJobNeverAssigned — the second control: a job that was NEVER
// assigned (still queued, no eligible account exists) has a genuinely NULL assigned_account_id —
// the real-world precondition platformapi's own attribution_test.go relies on when it asserts the
// LEFT-vs-INNER-JOIN behavior (§2.2) at the JSON-shape level using its own fixtures; this test
// proves dispatch actually produces that precondition rather than assuming it.
func TestAttribution_T5_Control_QueuedJobNeverAssigned(t *testing.T) {
	e := newDispatchEnv(t, "attrt5queued")
	ctx := context.Background()
	userID := e.seedUser(t, "attrt5queued@example.com")
	// No account at all for this user — the job can never be claimed into 'assigned'.
	jobID := e.seedQueuedJobFor(t, userID, "t5queued@example.com")

	if _, _, err := e.d.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var state string
	var assignedAcct *string
	if err := e.admin.QueryRow(ctx,
		`SELECT state::text, assigned_account_id::text FROM byosmtp.jobs WHERE id=$1`, jobID).Scan(&state, &assignedAcct); err != nil {
		t.Fatalf("read job: %v", err)
	}
	if state != "queued" {
		t.Fatalf("state=%s, want still queued (no account exists to claim it)", state)
	}
	if assignedAcct != nil {
		t.Fatalf("assigned_account_id=%v, want NULL — the job was never dispatched", *assignedAcct)
	}
}

// TestAttribution_T5_Control_TenancyNeverCrossesUsers — the third control: a real dispatch run
// never cross-assigns a job to another user's account — the structural guarantee
// accountConsume's own user_id predicate provides, which is what makes platformapi's LEFT JOIN
// (added on top of jobs.assigned_account_id, §2.2) safe without a tenancy check of its own: the
// column it reads can never have crossed a tenant boundary in the first place.
func TestAttribution_T5_Control_TenancyNeverCrossesUsers(t *testing.T) {
	e := newDispatchEnv(t, "attrt5tenancy")
	ctx := context.Background()
	userA := e.seedUser(t, "attrt5tenancyA@example.com")
	userB := e.seedUser(t, "attrt5tenancyB@example.com")
	acctB := e.seedAccountForUser(t, userB, "secB")
	e.setAccountDailyState(t, acctB, 1000, 0, todayUTC(), false, nil)
	// userA has NO account of their own.
	jobA := e.seedQueuedJobFor(t, userA, "tenancyA@example.com")

	if _, _, err := e.d.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var stateA string
	var assignedA *string
	if err := e.admin.QueryRow(ctx,
		`SELECT state::text, assigned_account_id::text FROM byosmtp.jobs WHERE id=$1`, jobA).Scan(&stateA, &assignedA); err != nil {
		t.Fatalf("read job A: %v", err)
	}
	if stateA != "queued" || assignedA != nil {
		t.Fatalf("user A's job state=%s assigned_account_id=%v, want queued/NULL — user B's account must never be cross-assigned", stateA, assignedA)
	}
}

// TestAttribution_T5_TotalsSumExactly — totals_by_account[] must sum EXACTLY to
// totals.sent/bounced/failed (§2.1's own cross-check). Drives real terminal outcomes through
// OutcomeHandler.Handle so the totals reflect genuine state transitions, not hand-set rows.
func TestAttribution_T5_TotalsSumExactly(t *testing.T) {
	e := newDispatchEnv(t, "attrt5totals")
	ctx := context.Background()
	userID := e.seedUser(t, "attrt5totals@example.com")
	acctID := e.seedAccountForUser(t, userID, "sec")
	e.setAccountDailyState(t, acctID, 1000, 0, todayUTC(), false, nil)

	var jobIDs []string
	for i := 0; i < 3; i++ {
		jobIDs = append(jobIDs, e.seedQueuedJobFor(t, userID, "t5t-"+string(rune('a'+i))+"@example.com"))
	}
	dispatched := 0
	for dispatched < len(jobIDs) {
		e.px.freeAllBoxes()
		_, pushed, err := e.d.Tick(ctx)
		if err != nil {
			t.Fatalf("tick: %v", err)
		}
		dispatched += pushed
		if pushed == 0 {
			break
		}
	}
	if dispatched != len(jobIDs) {
		t.Fatalf("dispatched %d of %d", dispatched, len(jobIDs))
	}

	// Two accepted (sent), one bounced — real outcomes through the real handler.
	e.oh.Handle(e.px.boxID, boxproto.Report{JobID: jobIDs[0], Outcome: boxproto.OutcomeAccepted, ProviderMessageID: "pmid-1"})
	e.oh.Handle(e.px.boxID, boxproto.Report{JobID: jobIDs[1], Outcome: boxproto.OutcomeAccepted, ProviderMessageID: "pmid-2"})
	e.oh.Handle(e.px.boxID, boxproto.Report{JobID: jobIDs[2], Outcome: boxproto.OutcomeBounced, ProviderMessageID: "pmid-3", SMTPResponse: "550 no such user"})

	var sent, bounced, failed int
	if err := e.admin.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE state='sent'), count(*) FILTER (WHERE state='bounced'), count(*) FILTER (WHERE state='failed')
		   FROM byosmtp.jobs WHERE user_id=$1`, userID).Scan(&sent, &bounced, &failed); err != nil {
		t.Fatalf("totals: %v", err)
	}

	var accountSent, accountBounced, accountFailed int
	if err := e.admin.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE state='sent'), count(*) FILTER (WHERE state='bounced'), count(*) FILTER (WHERE state='failed')
		   FROM byosmtp.jobs WHERE user_id=$1 AND assigned_account_id=$2`, userID, acctID).Scan(&accountSent, &accountBounced, &accountFailed); err != nil {
		t.Fatalf("per-account totals: %v", err)
	}

	if sent != accountSent || bounced != accountBounced || failed != accountFailed {
		t.Fatalf("totals(sent=%d,bounced=%d,failed=%d) != per-account sum(sent=%d,bounced=%d,failed=%d) — totals_by_account must sum exactly to totals",
			sent, bounced, failed, accountSent, accountBounced, accountFailed)
	}
	if sent != 2 || bounced != 1 || failed != 0 {
		t.Fatalf("got sent=%d bounced=%d failed=%d, want 2/1/0", sent, bounced, failed)
	}
}
