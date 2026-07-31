package boxlink

// Phase 7 review gate R3 — the one-in-flight marker vs a REPLAYED report (§2.5 × §1.3/§2.3).
//
// Phase 7 turned `report` from a once-per-job frame into a REPEATABLE one: the box replays every
// unacked outcome on each heartbeat tick and on each reconnect. dispatchFrame used to clear the box's
// in-flight marker on ANY report, so a replay for an OLD job cleared the marker of the NEW job the box
// was actually running — the dispatcher then saw the box as idle and pushed a SECOND job (with a
// SECOND decrypted credential) to a box already mid-SMTP. That breaks one-job-per-box (§2.5) and, since
// the box's #runJob is fire-and-forget, it genuinely runs two sessions holding two credentials.
//
// This exercises the REAL StreamServer state machine (real boxConn, real dispatchFrame, real PushJob,
// real AnyConnectedBox) over a real in-process socket pair. No DB is needed: the marker lives entirely
// in memory and the frames under test (report) take no DB path when onReport is the identity handler.

import (
	"encoding/json"
	"net"
	"testing"

	"github.com/byosmtp-module/brain/internal/boxproto"
)

// newMarkerServer builds a StreamServer with ONE registered box, bypassing only the TLS listener and
// the register handshake (both proven by the live-stack mTLS suite): everything the test asserts on —
// the boxes map, boxConn.inFlight, dispatchFrame, PushJob, AnyConnectedBox — is the production code.
func newMarkerServer(t *testing.T, boxID string, onReport ReportHandler) (*StreamServer, net.Conn) {
	t.Helper()
	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
	// Drain whatever the server writes so a frame write never blocks on the pipe.
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := client.Read(buf); err != nil {
				return
			}
		}
	}()
	s := &StreamServer{
		onReport: onReport,
		boxes:    make(map[string]*boxConn),
		closed:   make(chan struct{}),
	}
	s.boxes[boxID] = &boxConn{conn: server, enc: json.NewEncoder(server), boxID: boxID}
	return s, server
}

func frame(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	return b
}

// TestInFlightMarker_ReplayedReportDoesNotFreeALiveJob is the regression test for R3's HIGH finding.
//
// Sequence, exactly as production produces it: job-OLD's outcome went unacked (DB outage) and is
// buffered on the box; the outage ends; the Brain pushes job-NEW to that same box; mid-SMTP the box's
// heartbeat tick replays job-OLD's report. Asserts the replay does NOT make the box push-eligible.
//
// Paired positive control: the report for the job the box is ACTUALLY holding DOES clear the marker, so
// the guard cannot pass by simply never clearing (which would wedge every box after its first job).
func TestInFlightMarker_ReplayedReportDoesNotFreeALiveJob(t *testing.T) {
	const boxID = "box-marker"
	var reported []string
	s, _ := newMarkerServer(t, boxID, func(_ string, rep boxproto.Report) bool {
		reported = append(reported, rep.JobID)
		return true // persisted (the ack path is proven elsewhere; irrelevant to the marker)
	})

	// The box is holding job-NEW (the real push path sets the marker).
	if err := s.PushJob(boxproto.PushJob{Type: boxproto.TypePushJob, RequestID: "req-new", JobID: "job-NEW"}, boxID); err != nil {
		t.Fatalf("push job-NEW: %v", err)
	}
	if got := s.AnyConnectedBox(); got != "" {
		t.Fatalf("AnyConnectedBox=%q while the box holds job-NEW, want \"\" (one-in-flight, §2.5)", got)
	}

	// *** THE REPLAY ***: a buffered outcome for a DIFFERENT, older job arrives on the live stream.
	s.dispatchFrame(boxID, frame(t, boxproto.Report{
		Type: boxproto.TypeReport, RequestID: "req-old", JobID: "job-OLD", Outcome: boxproto.OutcomeAccepted,
		ProviderMessageID: "pmid-old", SMTPResponse: "250 2.0.0 OK",
	}))

	// The replay must still reach the outcome handler (it is a real outcome that may need persisting —
	// dropping it would lose it, and the box would replay forever).
	if len(reported) != 1 || reported[0] != "job-OLD" {
		t.Fatalf("replayed report reached the handler as %v, want [job-OLD] — the marker guard must not swallow the outcome", reported)
	}
	// ...but it must NOT have freed the live job's marker.
	s.mu.Lock()
	held := s.boxes[boxID].inFlight
	s.mu.Unlock()
	if held != "job-NEW" {
		t.Fatalf("in-flight marker is %q after a replayed report for job-OLD, want job-NEW — a stale replay freed a LIVE job's slot", held)
	}
	if got := s.AnyConnectedBox(); got != "" {
		t.Fatalf("AnyConnectedBox=%q after a replayed report for an old job — the dispatcher now believes a box mid-SMTP is idle", got)
	}
	// The concrete harm, asserted directly: a SECOND push (a second plaintext credential) would succeed.
	if err := s.PushJob(boxproto.PushJob{Type: boxproto.TypePushJob, RequestID: "req-2nd", JobID: "job-SECOND"}, boxID); err == nil {
		t.Fatal("PushJob ACCEPTED a second concurrent job after a replayed report — two jobs and TWO plaintext credentials on one box at once (§2.5 broken)")
	}

	// ---- POSITIVE CONTROL: the report for the job actually held DOES clear the marker. ----
	s.dispatchFrame(boxID, frame(t, boxproto.Report{
		Type: boxproto.TypeReport, RequestID: "req-new", JobID: "job-NEW", Outcome: boxproto.OutcomeAccepted,
		ProviderMessageID: "pmid-new", SMTPResponse: "250 2.0.0 OK",
	}))
	s.mu.Lock()
	held = s.boxes[boxID].inFlight
	s.mu.Unlock()
	if held != "" {
		t.Fatalf("in-flight marker is %q after the OWNING job's report, want \"\" — the box would never be pushed to again", held)
	}
	if got := s.AnyConnectedBox(); got != boxID {
		t.Fatalf("AnyConnectedBox=%q after the owning report, want %q — the box must become eligible again", got, boxID)
	}
	if err := s.PushJob(boxproto.PushJob{Type: boxproto.TypePushJob, RequestID: "req-3rd", JobID: "job-THIRD"}, boxID); err != nil {
		t.Fatalf("PushJob refused the next job after a clean completion: %v", err)
	}
}

// TestInFlightMarker_ReplayOnAnIdleBoxIsHarmless covers the other replay shape: the box finished its
// job, was acked-less, and replays while IDLE. The marker is already "" and must stay "" — the box
// remains eligible (a replay must not wedge a healthy idle box out of the dispatch pool).
func TestInFlightMarker_ReplayOnAnIdleBoxIsHarmless(t *testing.T) {
	const boxID = "box-idle"
	s, _ := newMarkerServer(t, boxID, func(_ string, _ boxproto.Report) bool { return true })

	if got := s.AnyConnectedBox(); got != boxID {
		t.Fatalf("precondition: AnyConnectedBox=%q, want %q (an idle registered box is eligible)", got, boxID)
	}
	s.dispatchFrame(boxID, frame(t, boxproto.Report{
		Type: boxproto.TypeReport, RequestID: "req-old", JobID: "job-OLD", Outcome: boxproto.OutcomeAccepted,
		ProviderMessageID: "pmid-old", SMTPResponse: "250 2.0.0 OK",
	}))
	s.mu.Lock()
	held := s.boxes[boxID].inFlight
	s.mu.Unlock()
	if held != "" {
		t.Fatalf("in-flight marker became %q from a replay on an IDLE box — the box is now permanently ineligible", held)
	}
	if got := s.AnyConnectedBox(); got != boxID {
		t.Fatalf("AnyConnectedBox=%q after an idle-box replay, want %q", got, boxID)
	}
}
