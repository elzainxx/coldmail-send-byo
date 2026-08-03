package dispatch

// provider_message_id_unique_test.go — the disposition of defect #140, asserted rather than argued.
//
// THE CLAIM (re-triaged HIGH): three individually-correct rulings interact into a wedge on the normal
// path. (1) The Message-ID is deterministic from job_id (§7.3 / messageIDFor), so a retry of the same
// job regenerates the SAME id. (2) A 250 with no parseable provider id records that generated
// Message-ID as provider_message_id (§4.3 / stream.js's `parseProviderID(...) || headers["Message-ID"]`).
// (3) byosmtp.jobs.provider_message_id is UNIQUE (byosmtp_0007). Therefore attempt 2 of a legitimately
// retried job should collide on the constraint and fail permanently.
//
// All three premises are TRUE — verified in the tree, not assumed. The CONCLUSION does not follow,
// and this file exists to pin the structural reason so a future refactor cannot quietly make it
// follow. Storing the id and becoming terminal are the SAME atomic UPDATE, guarded on
// `state='sending' AND provider_message_id IS NULL` (outcome.go's applyAccepted). A row therefore
// cannot hold a provider_message_id and still be retryable: every requeue path guards on
// `state='sending'` or `state IN ('assigned','sending')`, and the only statement that writes the
// column leaves the row 'sent'. "Attempt 1 stored X, then was retried" is unreachable by
// construction — which is exactly why the determinism refinement was safe to adopt.
//
// BUT the investigation found a REAL defect of the same family, one step to the side, and it is
// fixed in outcome.go alongside these tests. The tests below:
//   - Test 1 proves storing the id IS becoming terminal (the two facts are inseparable) — the
//     invariant that makes the claimed reproduction unreachable, asserted so a refactor cannot
//     quietly break it.
//   - Test 2 drives the claimed reproduction end-to-end and shows the second accepted report is a
//     clean no-op, not a 23505.
//   - Test 3 covers the real defect: a UNIQUE collision between two DIFFERENT jobs, reachable
//     whenever a provider does not mint a unique id per message. Unfixed it was worse than the
//     claim — an unackable outcome replayed forever, a lease that expires, reclaim, and a genuine
//     DOUBLE SEND. Fixed by falling back to the Brain Message-ID (P3-3's own ruling, one layer later).
//   - Test 4 is its negative control: a NON-collision commit failure must still go unacked, with a
//     paired positive control so "not acked" is evidence rather than an accident.

import (
	"context"
	"strings"
	"testing"

	"github.com/byosmtp-module/brain/internal/boxproto"
)

// Test 1 — the load-bearing invariant. If a future change ever lets a row carry a
// provider_message_id while still being retryable, #140 becomes real and THIS test is what catches
// it. Asserted directly rather than inferred from the SQL text.
func TestProviderMessageID_StoringItIsBecomingTerminal(t *testing.T) {
	e := newDispatchEnv(t, "pmidterm")
	ctx := context.Background()
	e.seedAccount(t, "sec")
	job := e.seedQueuedJob(t, "pmid-term@example.com")
	if _, err := e.d.TryDispatch(ctx); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// A 250 with NOTHING parseable is the §4.3 fallback path — precisely the case #140 is about,
	// where the recorded provider_message_id IS the deterministic Message-ID.
	if !e.oh.Handle(e.px.boxID, boxproto.Report{
		Type: boxproto.TypeReport, JobID: job, Outcome: boxproto.OutcomeAccepted,
		ProviderMessageID: messageIDFor(job, "u@ourdomain.com"), SMTPResponse: "250 OK",
	}) {
		t.Fatalf("first accepted report was not persist-acked")
	}

	state, hasPMID, _, _, _ := e.jobState(t, job)
	if !hasPMID {
		t.Fatalf("PRECONDITION FAILED: provider_message_id not stored, so this test proves nothing about the pairing")
	}
	if state != "sent" {
		t.Fatalf("a row holds a provider_message_id while in state %q — #140's premise is now REACHABLE: a retry path that guards on state (not on provider_message_id) can hand this job a second attempt, and the deterministic Message-ID will collide on the UNIQUE constraint", state)
	}

	// The other half: no requeue can move it back out of 'sent'. Both requeue statements guard on
	// state='sending' / IN ('assigned','sending'), so a terminal row is unreachable to them. Prove it
	// against the real statement shape rather than by reading the guard.
	tag, err := e.pool.Exec(ctx,
		`UPDATE byosmtp.jobs SET state='queued', lease_expires_at=NULL, next_attempt_at=NOW()
		  WHERE id=$1 AND state IN ('assigned','sending')`, job)
	if err != nil {
		t.Fatalf("requeue-shaped update: %v", err)
	}
	if tag.RowsAffected() != 0 {
		t.Fatalf("a requeue-shaped UPDATE matched %d rows on a 'sent' job — a job carrying a provider_message_id became retryable, which is exactly the wedge #140 describes", tag.RowsAffected())
	}
}

// Test 2 — the claimed reproduction, driven for real. A second accepted report carrying the SAME
// deterministic id (a replayed buffered outcome, or a box that re-reported) must be a clean no-op:
// persist-acked so the box drops it, no 23505, no state change.
func TestProviderMessageID_SameIDReportedTwiceIsCleanNoop(t *testing.T) {
	e := newDispatchEnv(t, "pmidtwice")
	ctx := context.Background()
	e.seedAccount(t, "sec")
	job := e.seedQueuedJob(t, "pmid-twice@example.com")
	if _, err := e.d.TryDispatch(ctx); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	deterministicID := messageIDFor(job, "u@ourdomain.com") // exactly what the box was handed (§8.1)
	rep := boxproto.Report{
		Type: boxproto.TypeReport, JobID: job, Outcome: boxproto.OutcomeAccepted,
		ProviderMessageID: deterministicID, SMTPResponse: "250 OK",
	}
	if !e.oh.Handle(e.px.boxID, rep) {
		t.Fatalf("first accepted report was not persist-acked")
	}
	events1 := e.eventCount(t, job)
	logs1 := e.sendLogCount(t, job)

	// The SAME id again. Pre-#140-analysis this was the feared 23505.
	if !e.oh.Handle(e.px.boxID, rep) {
		t.Fatalf("the SECOND report of the same provider_message_id was NOT persist-acked — the box will RETAIN and REPLAY this outcome indefinitely. If the cause is a 23505 on provider_message_id, #140 is real")
	}

	state, _, _, _, _ := e.jobState(t, job)
	if state != "sent" {
		t.Fatalf("after a duplicate accepted report: state=%q, want sent (unchanged)", state)
	}
	if got := e.sendLogCount(t, job); got != logs1 {
		t.Fatalf("duplicate report wrote a second send_log (%d → %d) — a double-send record for one real send", logs1, got)
	}
	if got := e.eventCount(t, job); got != events1 {
		t.Fatalf("duplicate report wrote %d extra job_events (%d → %d) — the no-op is not clean", got-events1, events1, got)
	}
}

// Test 3 — THE REAL DEFECT #140 turned up, and the one worth fixing. The tests above pass because
// the SAME job cannot collide with itself. They say nothing about two DIFFERENT jobs carrying one
// provider id — which IS reachable: parseProviderID (stream.js) extracts whatever the provider
// echoes via three loose patterns, so a provider (or a relay/sink in front of one) whose 250 line
// carries a CONSTANT token in one of those positions hands back the same id for every message, and
// the second job to report it raises a genuine 23505.
//
// Before the fix that was the worst available outcome: the 23505 is not errNoop, so persisted()
// returned false, so the box RETAINED and REPLAYED the outcome — deterministically forever, since
// the collision is not transient — while the job row stayed 'sending'. Its lease (120s) expires long
// before the box's buffer TTL (900s), so reclaim requeued it and it was SENT AGAIN: a real 250
// turned into a real duplicate delivery, the exact thing this column exists to prevent.
//
// The fix is P3-3's own ruled fallback applied one layer later — the box falls back to the Brain
// Message-ID when it cannot PARSE an id, the Brain falls back when the parsed id turns out not to be
// UNIQUE. This test asserts the CONSEQUENCE (acked, terminal, one send_log, the provider's raw text
// still preserved), not the mechanism, so it stays valid under any implementation that resolves the
// collision.
func TestProviderMessageID_CrossJobCollisionFallsBackToBrainMessageID(t *testing.T) {
	e := newDispatchEnv(t, "pmidcross")
	ctx := context.Background()
	e.seedAccount(t, "sec")

	jobA := e.seedQueuedJob(t, "pmid-cross-a@example.com")
	if _, err := e.d.TryDispatch(ctx); err != nil {
		t.Fatalf("dispatch A: %v", err)
	}
	const sharedID = "provider-echoed-the-same-id-twice"
	if !e.oh.Handle(e.px.boxID, boxproto.Report{
		Type: boxproto.TypeReport, JobID: jobA, Outcome: boxproto.OutcomeAccepted,
		ProviderMessageID: sharedID, SMTPResponse: "250 OK queued as " + sharedID,
	}) {
		t.Fatalf("job A's accepted report was not persist-acked")
	}
	if stateA, _, _, _, _ := e.jobState(t, jobA); stateA != "sent" {
		t.Fatalf("PRECONDITION FAILED: job A is %q, want sent — without a stored id there is nothing for B to collide with", stateA)
	}
	if got := e.providerMessageID(t, jobA); got != sharedID {
		t.Fatalf("PRECONDITION FAILED: job A stored %q, want the provider's own id %q — the FIRST job must keep the provider id, the fallback is only for the loser of a collision", got, sharedID)
	}

	// Job B is a DIFFERENT job that reports the SAME provider id.
	e.px.freeAllBoxes()
	jobB := e.seedQueuedJob(t, "pmid-cross-b@example.com")
	if _, err := e.d.TryDispatch(ctx); err != nil {
		t.Fatalf("dispatch B: %v", err)
	}
	const respB = "250 OK queued as " + sharedID
	acked := e.oh.Handle(e.px.boxID, boxproto.Report{
		Type: boxproto.TypeReport, JobID: jobB, Outcome: boxproto.OutcomeAccepted,
		ProviderMessageID: sharedID, SMTPResponse: respB,
	})

	stateB, hasPMIDB, leaseNullB, completedB, _ := e.jobState(t, jobB)
	if !acked {
		t.Fatalf("job B's accepted report was NOT persist-acked after a cross-job provider id collision (state=%q) — the box will RETAIN and REPLAY this outcome forever, its lease will expire, reclaim will requeue it, and the message will be SENT TWICE. This is defect #140's real hazard", stateB)
	}
	if stateB != "sent" {
		t.Fatalf("job B is %q after a collision, want sent — the box was told the outcome is durably resolved, so the row MUST be terminal or the ack is a lie (Rule 5)", stateB)
	}
	if !hasPMIDB {
		t.Fatalf("job B is 'sent' with a NULL provider_message_id — the idempotency key is missing, so a replay has nothing to collide with")
	}
	if !leaseNullB || !completedB {
		t.Fatalf("job B: lease_expires_at cleared=%v, completed_at set=%v — want true/true; a 'sent' row with a live lease is reclaimable", leaseNullB, completedB)
	}

	// The substituted key must be the Brain-generated Message-ID (deterministic from job_id, §8.1) —
	// unique per job by construction, and the same value already recorded in send_logs.headers.
	gotB := e.providerMessageID(t, jobB)
	if gotB == sharedID {
		t.Fatalf("job B stored the colliding id %q — the UNIQUE constraint did not hold, a far worse finding than #140 claimed", sharedID)
	}
	if want := messageIDFor(jobB, "u@ourdomain.com"); gotB != want {
		t.Fatalf("job B stored provider_message_id %q, want the Brain Message-ID %q — the fallback must be the deterministic job-seeded id, not an arbitrary new value", gotB, want)
	}

	// Nothing is invented and nothing is lost (Rule 5): the provider's raw 250 — including its own
	// id — is still recorded verbatim, so the substitution costs no diagnostic information.
	if n := e.sendLogCount(t, jobB); n != 1 {
		t.Fatalf("job B has %d send_logs rows, want exactly 1 — the fallback retry must not double-write the attempt record", n)
	}
	if resp := e.sendLogResponse(t, jobB); !strings.Contains(resp, sharedID) {
		t.Fatalf("job B's send_logs.smtp_response is %q and no longer contains the provider's own id %q — the substitution discarded the provider's raw text instead of preserving it", resp, sharedID)
	}

	// A replay of the SAME colliding report must still be a clean no-op — the fallback must not open a
	// path where every replay re-enters the collision branch.
	if !e.oh.Handle(e.px.boxID, boxproto.Report{
		Type: boxproto.TypeReport, JobID: jobB, Outcome: boxproto.OutcomeAccepted,
		ProviderMessageID: sharedID, SMTPResponse: respB,
	}) {
		t.Fatalf("a REPLAY of job B's colliding report was not acked — the box would replay it forever")
	}
	if n := e.sendLogCount(t, jobB); n != 1 {
		t.Fatalf("after a replay job B has %d send_logs rows, want 1 — the replay was not idempotent", n)
	}
}

// The negative control for Test 3. The fallback must fire ONLY on a provider_message_id collision —
// not on any other error from the same statement. A DB error that genuinely prevented the commit
// must still leave the outcome unacked (retain + replay), because acking it would claim a durable
// outcome that does not exist (Rule 5). Driven by making the job's own row unwritable, so the
// UPDATE fails for a reason that is NOT a 23505 on this column.
func TestProviderMessageID_NonCollisionErrorIsStillNotAcked(t *testing.T) {
	e := newDispatchEnv(t, "pmidnoncol")
	ctx := context.Background()
	e.seedAccount(t, "sec")
	job := e.seedQueuedJob(t, "pmid-noncol@example.com")
	if _, err := e.d.TryDispatch(ctx); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// A BEFORE UPDATE trigger that raises a non-23505 error is the cleanest way to make this one
	// statement fail for an unrelated reason while the DB is otherwise healthy.
	if _, err := e.admin.Exec(ctx, `
		CREATE FUNCTION byosmtp.pmid_test_block() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'pmid test: unrelated failure' USING ERRCODE = '23514'; END $$;
		CREATE TRIGGER trg_pmid_test_block BEFORE UPDATE ON byosmtp.jobs
		  FOR EACH ROW WHEN (NEW.state = 'sent') EXECUTE FUNCTION byosmtp.pmid_test_block();`); err != nil {
		t.Fatalf("install blocking trigger: %v", err)
	}

	acked := e.oh.Handle(e.px.boxID, boxproto.Report{
		Type: boxproto.TypeReport, JobID: job, Outcome: boxproto.OutcomeAccepted,
		ProviderMessageID: "pmid-noncol-unique-id", SMTPResponse: "250 OK",
	})
	if acked {
		t.Fatalf("an accepted report whose commit FAILED (non-collision error) was persist-acked — the Brain claimed a durable outcome it does not have (Rule 5), and the box has dropped the only copy")
	}
	if state, hasPMID, _, _, _ := e.jobState(t, job); state != "sending" || hasPMID {
		t.Fatalf("after a failed commit: state=%q hasPMID=%v, want sending/false — the row must be exactly as stored", state, hasPMID)
	}

	// Positive control: with the trigger gone, the very same report commits and acks. Without this,
	// "not acked" above could be caused by anything.
	if _, err := e.admin.Exec(ctx,
		`DROP TRIGGER trg_pmid_test_block ON byosmtp.jobs; DROP FUNCTION byosmtp.pmid_test_block();`); err != nil {
		t.Fatalf("drop blocking trigger: %v", err)
	}
	if !e.oh.Handle(e.px.boxID, boxproto.Report{
		Type: boxproto.TypeReport, JobID: job, Outcome: boxproto.OutcomeAccepted,
		ProviderMessageID: "pmid-noncol-unique-id", SMTPResponse: "250 OK",
	}) {
		t.Fatalf("POSITIVE CONTROL FAILED: the same report was not acked with the trigger removed, so the not-acked assertion above proves nothing")
	}
	if state, hasPMID, _, _, _ := e.jobState(t, job); state != "sent" || !hasPMID {
		t.Fatalf("positive control: state=%q hasPMID=%v, want sent/true", state, hasPMID)
	}
	if got := e.providerMessageID(t, job); got != "pmid-noncol-unique-id" {
		t.Fatalf("positive control stored %q, want the provider's own id — a NON-colliding id must never be substituted", got)
	}
}

// providerMessageID reads the stored idempotency key (empty string when NULL). jobState only
// reports whether one is set; the collision-fallback tests need the VALUE.
func (e *dispatchEnv) providerMessageID(t *testing.T, jobID string) string {
	t.Helper()
	var pmid *string
	if err := e.admin.QueryRow(context.Background(),
		`SELECT provider_message_id FROM byosmtp.jobs WHERE id=$1`, jobID).Scan(&pmid); err != nil {
		t.Fatalf("read provider_message_id: %v", err)
	}
	if pmid == nil {
		return ""
	}
	return *pmid
}

// sendLogResponse reads the recorded (already box-scrubbed) provider text for a job's send_logs row.
func (e *dispatchEnv) sendLogResponse(t *testing.T, jobID string) string {
	t.Helper()
	var resp string
	if err := e.admin.QueryRow(context.Background(),
		`SELECT COALESCE(smtp_response,'') FROM byosmtp.send_logs WHERE job_id=$1`, jobID).Scan(&resp); err != nil {
		t.Fatalf("read send_logs.smtp_response: %v", err)
	}
	return resp
}

// The three premises themselves, asserted so the analysis above cannot rot. If any of these stops
// being true, #140's disposition must be re-derived from scratch rather than trusted.
func TestProviderMessageID_140PremisesStillHold(t *testing.T) {
	// (1) deterministic from job_id.
	if a, b := messageIDFor("job-140", "s@example.com"), messageIDFor("job-140", "s@example.com"); a != b {
		t.Fatalf("premise 1 broken: messageIDFor is no longer deterministic (%q != %q)", a, b)
	}
	// (2) the fallback records that generated id — asserted at the box's own source, since the
	// fallback lives in stream.js and this package cannot call it. The shape is what matters:
	// applyAccepted must accept an id that LOOKS like a Message-ID without special-casing it.
	if id := messageIDFor("job-140", "s@example.com"); !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, ">") {
		t.Fatalf("premise 2 broken: the generated Message-ID %q is no longer the angle-bracketed form stream.js falls back to", id)
	}
	// (3) the column is UNIQUE — read from the live catalog, not from the migration text.
	e := newDispatchEnv(t, "pmidprem")
	var uniqueCount int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM pg_index i
		   JOIN pg_class c ON c.oid = i.indrelid
		   JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = ANY(i.indkey)
		  WHERE c.relname = 'jobs' AND a.attname = 'provider_message_id'
		    AND i.indisunique AND i.indnatts = 1`).Scan(&uniqueCount); err != nil {
		t.Fatalf("read unique indexes on jobs.provider_message_id: %v", err)
	}
	if uniqueCount != 1 {
		t.Fatalf("premise 3 broken: %d single-column UNIQUE indexes on jobs.provider_message_id, want 1 — #140's whole analysis assumed this constraint exists", uniqueCount)
	}
}
