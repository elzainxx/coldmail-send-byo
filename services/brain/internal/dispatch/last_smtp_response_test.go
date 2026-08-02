package dispatch

// last_smtp_response_test.go — Phase 7 §1.4: jobs.last_smtp_response carries the PROVIDER TEXT
// ALONE, never last_error_message's internal-slug-prefixed concatenation
// (rep.ErrorReason + ": " + rep.SMTPResponse). This is the fix for the exact gap §1.4 names: a
// user-facing surface reading last_error_message on the first failure of a campaign would
// serialize an internal reason slug to a client — a two-vocabulary violation arriving through
// reusing the column that "looks right," not through carelessness.
//
// Real Dispatcher + OutcomeHandler, real Postgres (CLAUDE.md Rule 3) — mirrors
// job_events_error_context_test.go's own harness usage.

import (
	"context"
	"testing"

	"github.com/byosmtp-module/brain/internal/boxproto"
)

// jobLastSmtpResponse reads the job row's last_smtp_response, "" when NULL.
func (e *dispatchEnv) jobLastSmtpResponse(t *testing.T, jobID string) string {
	t.Helper()
	var resp *string
	if err := e.admin.QueryRow(context.Background(),
		`SELECT last_smtp_response FROM byosmtp.jobs WHERE id=$1`, jobID).Scan(&resp); err != nil {
		t.Fatalf("read jobs.last_smtp_response: %v", err)
	}
	if resp == nil {
		return ""
	}
	return *resp
}

// TestLastSmtpResponse_ClassifiedFailureCarriesProviderTextAlone — a real transport_4xx failure
// (a classified requeue, applyClassifiedFailure) must populate last_smtp_response with EXACTLY
// the provider's own text and NOTHING else — critically, NOT last_error_message's
// ErrorReason-prefixed form. The two columns must therefore DIFFER: last_error_message carries
// the internal slug + provider text; last_smtp_response carries only the provider text.
func TestLastSmtpResponse_ClassifiedFailureCarriesProviderTextAlone(t *testing.T) {
	e := newDispatchEnv(t, "lastsmtpresp")
	e.seedAccount(t, "sec")
	job := e.seedQueuedJob(t, "lastsmtpresp@example.com")

	const providerText = "451 4.3.0 Temporary local error in processing"
	e.dispatchToSending(t, job)
	e.oh.Handle(e.px.boxID, boxproto.Report{JobID: job, Outcome: boxproto.OutcomeError,
		SMTPResponse: providerText})

	if state, _, _, _, _ := e.jobState(t, job); state != "queued" {
		t.Fatalf("after the 451 the job is %s, want queued — this test needs a REAL requeue to have happened", state)
	}

	gotResponse := e.jobLastSmtpResponse(t, job)
	if gotResponse != providerText {
		t.Fatalf("jobs.last_smtp_response=%q, want exactly %q (the provider text alone)", gotResponse, providerText)
	}

	gotMessage := e.jobLastErrorClass(t, job) // sanity: the class landed as expected
	if gotMessage != "transport_4xx" {
		t.Fatalf("jobs.last_error_class=%q, want transport_4xx — classify.go routed this text elsewhere, so the rest of this test would be measuring a different path", gotMessage)
	}

	// THE ASSERTION THAT MATTERS: last_error_message carries the ErrorReason-prefixed
	// concatenation and therefore DIFFERS from last_smtp_response. If a future change made them
	// equal again, it would mean either last_smtp_response started carrying the slug (a leak) or
	// last_error_message stopped carrying it (an internal-diagnosis regression) — this control
	// distinguishes both from the correct, deliberately-different state.
	var fullMessage string
	if err := e.admin.QueryRow(context.Background(),
		`SELECT last_error_message FROM byosmtp.jobs WHERE id=$1`, job).Scan(&fullMessage); err != nil {
		t.Fatalf("read jobs.last_error_message: %v", err)
	}
	if fullMessage == gotResponse {
		t.Fatalf("last_error_message (%q) equals last_smtp_response (%q) — expected them to differ (the internal slug prefix should be present in one and absent from the other)", fullMessage, gotResponse)
	}
	if fullMessage != "unknown: "+providerText && fullMessage != "data_rejected_4xx: "+providerText {
		// The exact ErrorReason slug for a bare-box-reported OutcomeError depends on classify.go's
		// own reason mapping for this report shape; assert the STRUCTURAL property (provider text
		// is a suffix, something precedes it) rather than pin one specific slug value, since the
		// slug is not this test's subject — the separation between the two columns is.
		if len(fullMessage) <= len(providerText) || fullMessage[len(fullMessage)-len(providerText):] != providerText {
			t.Fatalf("last_error_message=%q does not end with the provider text %q — expected ErrorReason + \": \" + SMTPResponse shape", fullMessage, providerText)
		}
	}
}

// TestLastSmtpResponse_BouncePermanentCarriesProviderTextAlone — a bounce writes raw
// rep.SMTPResponse into BOTH last_error_message and last_smtp_response (applyBounced never
// concatenates a slug — bounce_permanent's message IS just the provider text), so the two columns
// are equal here, unlike the classified-failure case above. This is the control that proves the
// difference above comes from the concatenation, not from last_smtp_response being wired wrong.
func TestLastSmtpResponse_BouncePermanentCarriesProviderTextAlone(t *testing.T) {
	e := newDispatchEnv(t, "lastsmtpbounce")
	e.seedAccount(t, "sec")
	job := e.seedQueuedJob(t, "lastsmtpbounce@example.com")

	const providerText = "550 5.1.1 No such user here"
	e.dispatchToSending(t, job)
	e.oh.Handle(e.px.boxID, boxproto.Report{JobID: job, Outcome: boxproto.OutcomeBounced,
		SMTPResponse: providerText})

	if state, _, _, _, _ := e.jobState(t, job); state != "bounced" {
		t.Fatalf("after the 550 the job is %s, want bounced", state)
	}

	gotResponse := e.jobLastSmtpResponse(t, job)
	if gotResponse != providerText {
		t.Fatalf("jobs.last_smtp_response=%q, want exactly %q", gotResponse, providerText)
	}
	var gotMessage string
	if err := e.admin.QueryRow(context.Background(),
		`SELECT last_error_message FROM byosmtp.jobs WHERE id=$1`, job).Scan(&gotMessage); err != nil {
		t.Fatalf("read jobs.last_error_message: %v", err)
	}
	if gotMessage != providerText {
		t.Fatalf("jobs.last_error_message=%q, want exactly %q (bounce never concatenates a slug)", gotMessage, providerText)
	}
	if gotMessage != gotResponse {
		t.Fatalf("last_error_message (%q) and last_smtp_response (%q) should be EQUAL on a bounce (control), but differ", gotMessage, gotResponse)
	}
}

// TestLastSmtpResponse_NullOnNoProviderText — a failure with NO provider response at all (e.g. a
// connect-level fault) must leave last_smtp_response as SQL NULL, not an empty string — the
// column's own nullability distinguishes "no response text at all" from "the provider replied
// with nothing."
func TestLastSmtpResponse_NullOnNoProviderText(t *testing.T) {
	e := newDispatchEnv(t, "lastsmtpnull")
	e.seedAccount(t, "sec")
	job := e.seedQueuedJob(t, "lastsmtpnull@example.com")

	e.dispatchToSending(t, job)
	e.oh.Handle(e.px.boxID, boxproto.Report{JobID: job, Outcome: boxproto.OutcomeError,
		ErrorReason: "connect_refused", SMTPResponse: ""})

	if state, _, _, _, _ := e.jobState(t, job); state != "queued" {
		t.Fatalf("after the connect_refused the job is %s, want queued", state)
	}

	var resp *string
	if err := e.admin.QueryRow(context.Background(),
		`SELECT last_smtp_response FROM byosmtp.jobs WHERE id=$1`, job).Scan(&resp); err != nil {
		t.Fatalf("read jobs.last_smtp_response: %v", err)
	}
	if resp != nil {
		t.Fatalf("jobs.last_smtp_response=%q, want SQL NULL (no provider text was ever received)", *resp)
	}
}
