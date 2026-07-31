package dispatch

import (
	"context"
	"testing"

	"github.com/byosmtp-module/brain/internal/boxproto"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPersistAck_ResolvedOnlyAfterCommit (Phase 7 §1.3, P7-4 — the persist-BEFORE-ack contract,
// Brain side) — Handle returns resolved=true ONLY when the outcome is durably committed (or a clean
// idempotent no-op the Brain owns), and resolved=false when it cannot determine/commit. The stream
// server keys report_ack on exactly this bool, so this proves the box is told to drop its buffered
// outcome only after a real DB commit.
func TestPersistAck_ResolvedOnlyAfterCommit(t *testing.T) {
	e := newDispatchEnv(t, "persistack")
	ctx := context.Background()
	pool := e.seedPool(t)
	e.seedAccount(t, pool, "sec")

	// (1) Accepted report on a real in-flight job → committed 'sent' → resolved=true.
	job := e.seedQueuedJob(t, "ack@example.com")
	e.dispatchToSending(t, job)
	if resolved := e.oh.Handle(e.px.boxID, boxproto.Report{JobID: job, Outcome: boxproto.OutcomeAccepted,
		ProviderMessageID: "PMID-ACK-1", SMTPResponse: "250 2.0.0 Ok"}); !resolved {
		t.Fatal("accepted report: resolved=false, want true (it committed sent — box may drop the buffer)")
	}
	if st, hasPMID, _, _, _ := e.jobState(t, job); st != "sent" || !hasPMID {
		t.Fatalf("job state=%s hasPMID=%v, want sent/true (the commit that justified the ack)", st, hasPMID)
	}

	// (2) A duplicate/replay of the SAME accepted report → clean idempotent no-op → resolved=true
	//     (the Brain owns it; a replay must still be ackable so the box stops replaying). No 2nd row.
	before := e.sendLogCount(t, job)
	if resolved := e.oh.Handle(e.px.boxID, boxproto.Report{JobID: job, Outcome: boxproto.OutcomeAccepted,
		ProviderMessageID: "PMID-ACK-1", SMTPResponse: "250 2.0.0 Ok"}); !resolved {
		t.Fatal("replayed accepted report: resolved=false, want true (idempotent no-op is ackable)")
	}
	if after := e.sendLogCount(t, job); after != before {
		t.Fatalf("replay wrote %d extra send_logs row(s), want 0 (idempotent)", after-before)
	}

	// (3) A report for a job that already left 'sending' (reclaimed to queued) → clean no-op →
	//     resolved=true (the Brain resolved it another way; the box may drop it).
	e.px.freeAllBoxes() // case 1's box is still marked in-flight on the recording pusher
	other := e.seedQueuedJob(t, "ack2@example.com")
	e.dispatchToSending(t, other)
	e.expireLease(t, other)
	e.px.freeAllBoxes()
	if _, n, err := e.d.ReclaimTick(ctx); err != nil || n != 1 {
		t.Fatalf("prime reclaim: n=%d err=%v, want 1/nil", n, err)
	}
	if resolved := e.oh.Handle(e.px.boxID, boxproto.Report{JobID: other, Outcome: boxproto.OutcomeAccepted,
		ProviderMessageID: "PMID-ACK-LATE", SMTPResponse: "250 2.0.0 Ok"}); !resolved {
		t.Fatal("report for a reclaimed job: resolved=false, want true (Brain owns it — box may drop)")
	}
	// It stayed queued (the late accepted did NOT resurrect it — Rule 5, no invented outcome).
	if st, hasPMID, _, _, _ := e.jobState(t, other); st != "queued" || hasPMID {
		t.Fatalf("reclaimed job state=%s hasPMID=%v, want queued/false (no resurrection)", st, hasPMID)
	}

	// (4) NEGATIVE control — a DB failure means Handle cannot determine the job state, so it must
	//     return resolved=false (NO ack → the box retains + replays). We simulate an unreachable DB by
	//     pointing a handler at a closed pool. This is the DB-outage path (§2) proven at unit level:
	//     the Brain never acks an outcome it could not persist (Rule 5).
	deadPool, err := pgxpool.New(ctx, e.moduleDSN)
	if err != nil {
		t.Fatalf("dead pool: %v", err)
	}
	deadPool.Close() // every query on it now fails — a stand-in for "DB unreachable"
	deadHandler := NewOutcomeHandler(deadPool, e.cfg)
	if resolved := deadHandler.Handle(e.px.boxID, boxproto.Report{JobID: job, Outcome: boxproto.OutcomeAccepted,
		ProviderMessageID: "PMID-ACK-DEAD", SMTPResponse: "250 2.0.0 Ok"}); resolved {
		t.Fatal("report under a dead DB: resolved=true, want false (NO ack — box must retain + replay; never ack an unpersisted outcome)")
	}
}
