package platformapi

// Phase 4 A1 (the platform's Phase 3 follow-up): GET /v1/report?campaign_id=...&cursor=... answers a cursor that DECODES but is not what this module issues with a 400 validation_failed, never a 500.
// Module 2 does the same since its close-out B2. Real HTTP against the production Server and a real Postgres clone (Rule 3).
//
// THE DEFECT. parseReportCursor checked that the cursor was base64 and had two `|`-separated parts, and nothing else: "forged|forged" reached the keyset query as text, the SQL cast failed and the
// caller got `500 internal_error` for a request that was theirs to get wrong. The platform's contract drift gate recorded that 500 as module 3's answer (research m3-api 6) and its adapter reported it as
// ErrBadCursor "a 500 on a request that carried a cursor". The fix checks both fields BEFORE they can reach SQL: an RFC 3339 instant and a canonical uuid, exactly what encodeReportCursor makes.
//
// THE CONTROLS (Rule 10 of the repository: a test that cannot fail is not a test). A valid cursor is still accepted (200, the same campaign), so the 400s are the validation and not a campaign that
// answers 400 to anything; every cursor this module can issue (whole seconds, microseconds, nanoseconds) is accepted by the parser, so no cursor already in a caller's hand is broken; a cursor that is not
// base64 still gets the 400 it always got.

import (
	"encoding/base64"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestReportCursor_ADecodableButForgedCursorIsA400NotA500(t *testing.T) {
	e := newTestEnv(t, "rep_cursor_forged")
	user := e.seedUser(t, "cursor-forged@example.com")
	const campaign = "11111111-2222-4333-8444-555555555555" // a campaign nobody ran: the report is an empty 200 (campaign_report.go), so only the cursor decides the status here
	const someID = "22222222-3333-4444-8555-666666666666"
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	call := func(cursor string) resp {
		return e.do(t, "GET", "/v1/report?campaign_id="+campaign+"&user_id="+user+"&cursor="+url.QueryEscape(cursor), nil)
	}

	// the control: a cursor of the shape this module issues is accepted, for the same campaign and user
	good := encodeReportCursor("2026-01-02T03:04:05.123456Z", someID)
	call(good).requireStatus(t, 200)

	for _, c := range []struct{ what, cursor string }{
		{"two words", b64("forged|forged")},
		{"an instant and an id that is not a uuid", b64("2026-01-02T03:04:05Z|not-a-uuid")},
		{"a uuid and an instant that is not an instant", b64("yesterday|" + someID)},
		{"a date and time with no zone", b64("2026-01-02 03:04:05|" + someID)},
		{"a date only", b64("2026-01-02|" + someID)},
		{"SQL in the id", b64("2026-01-02T03:04:05Z|x'; DROP TABLE send_byo.jobs; --")},
		{"an extra separator", b64("2026-01-02T03:04:05Z|" + someID + "|extra")},
		{"an empty instant", b64("|" + someID)},
		{"an empty id", b64("2026-01-02T03:04:05Z|")},
		{"a number", b64("1767323045|" + someID)},
	} {
		r := call(c.cursor).requireStatus(t, 400)
		if r.errorCode() != "validation_failed" {
			t.Errorf("%s: error code %q, want validation_failed: %s", c.what, r.errorCode(), r.raw)
		}
		if got := r.errorDetailFields(); len(got) != 1 || got[0] != "cursor:malformed" {
			t.Errorf("%s: details %v, want [cursor:malformed]: %s", c.what, got, r.raw)
		}
	}

	// the old path is unchanged: a cursor that is not base64 at all
	r := call("not base64 !!").requireStatus(t, 400)
	if got := r.errorDetailFields(); len(got) != 1 || got[0] != "cursor:malformed" {
		t.Errorf("a cursor that is not base64: details %v, want [cursor:malformed]", got)
	}
}

// TestParseReportCursor_EveryCursorTheModuleIssuesIsAccepted: the validation must not strand a cursor a caller already holds. encodeReportCursor is what builds every next_cursor; its input is
// time.RFC3339Nano of a UTC instant (campaign_report.go), so the instants below are the forms it can produce.
func TestParseReportCursor_EveryCursorTheModuleIssuesIsAccepted(t *testing.T) {
	const id = "22222222-3333-4444-8555-666666666666"
	for _, ts := range []time.Time{
		time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC),
		time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC),
		time.Date(2026, 12, 31, 23, 59, 59, 999999999, time.UTC),
	} {
		at := ts.UTC().Format(time.RFC3339Nano)
		req := httptest.NewRequest("GET", "/v1/report?cursor="+url.QueryEscape(encodeReportCursor(at, id)), nil)
		cur, aerr := parseReportCursor(req)
		if aerr != nil || cur == nil || cur.CreatedAt != at || cur.ID != id {
			t.Errorf("a cursor for %s was refused or changed: %+v, %v", at, cur, aerr)
		}
	}
	// the control the other way: the same parser refuses the forged one (so the loop above is not accepting everything)
	req := httptest.NewRequest("GET", "/v1/report?cursor="+url.QueryEscape(encodeReportCursor("forged", "forged")), nil)
	if cur, aerr := parseReportCursor(req); aerr == nil || cur != nil {
		t.Errorf("a forged cursor was accepted: %+v", cur)
	}
}
