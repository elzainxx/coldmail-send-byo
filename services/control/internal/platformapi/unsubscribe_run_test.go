package platformapi

// unsubscribe_run_test.go — Phase 5 S4, the Domain-A half: POST /v1/run copies the platform's per-recipient
// public.job_items.unsubscribe_url into send_byo.jobs.unsubscribe_url in the SAME statement that copies rendered_body and
// rendered_text (J2); the module's own CHECK (jobs_unsubscribe_url_check, send_byo_0060) refuses what a header cannot carry;
// and describe advertises the capability.
//
// NO field is added to the run request: the link rides on the platform-owned job_items row, exactly like the HTML body.
//
// Per behaviour, with its paired control:
//   - a link present -> the job row holds it VERBATIM; CONTROL: a NULL link -> NULL (an absent link stays absent, so the message
//     carries neither header);
//   - the run request body is unchanged: an `unsubscribe_url` field on it is an unknown field and a 400 (CONTROL: the same body
//     without it is accepted and the job carries the PLATFORM's link);
//   - a link the header cannot carry (http, a space, `<`, `>`, CR, LF, TAB, a control byte, one character too long, a scheme alone)
//     is refused 400 validation_failed and NO job row is written; the paired control is the at-limit link, accepted and stored whole,
//     and the same item is runnable again once its link is fixed (the refusal is the value, nothing sticky);
//   - describe.capabilities.list_unsubscribe is true, and every capability that was there still is.

import (
	"strings"
	"testing"
)

// seedJobItemLink inserts a public.job_items row carrying unsubscribe_url (nil = NULL) and returns its id.
func (e *testEnv) seedJobItemLink(t *testing.T, userID, recipient string, link *string) string {
	t.Helper()
	var id string
	if err := e.admin.QueryRow(bgctx(),
		`INSERT INTO public.job_items (campaign_id, user_id, recipient_email, rendered_subject, rendered_body, unsubscribe_url, reply_to, status)
		 VALUES ($1, $1, $2, 'Subj', '<p>b</p>', $3, 'reply@ourdomain.com', 'pending') RETURNING id`,
		userID, recipient, link).Scan(&id); err != nil {
		t.Fatalf("seed job_item with unsubscribe_url: %v", err)
	}
	return id
}

func (e *testEnv) jobUnsubscribeURL(t *testing.T, jobID string) *string {
	t.Helper()
	var u *string
	if err := e.admin.QueryRow(bgctx(), `SELECT unsubscribe_url FROM send_byo.jobs WHERE id=$1`, jobID).Scan(&u); err != nil {
		t.Fatalf("read jobs.unsubscribe_url: %v", err)
	}
	return u
}

const unsubProbeLink = "https://unsubscribe.example.test/u/AbC-123_xyz"

func TestRun_CopiesTheUnsubscribeLinkVerbatim_AndNullStaysNull(t *testing.T) {
	e := newTestEnv(t, "ulcopy")
	user := e.seedUser(t, "ulcopy@example.com")

	// Each case is a DIFFERENT job_item of the SAME campaign (campaign_id = user id), so the one-active-campaign rule never interferes.
	cases := []struct {
		name string
		link *string
	}{
		{"a plain https link", ptr(unsubProbeLink)},
		{"a query, a fragment and a percent escape are kept verbatim", ptr(unsubProbeLink + "?a=b&c=%20#frag")},
		{"absent (NULL)", nil},
	}
	for i, c := range cases {
		item := e.seedJobItemLink(t, user, "ulcopy"+strings.Repeat("x", i)+"@example.com", c.link)
		r := e.runItem(t, user, item).requireStatus(t, 202)
		got := e.jobUnsubscribeURL(t, r.str("job_id"))
		switch {
		case c.link == nil && got != nil:
			t.Fatalf("%s: jobs.unsubscribe_url = %q, want NULL (an absent link must stay absent: the message then has neither header)", c.name, *got)
		case c.link != nil && (got == nil || *got != *c.link):
			t.Fatalf("%s: jobs.unsubscribe_url = %v, want %q verbatim", c.name, got, *c.link)
		}
	}
}

func TestRun_TheRequestBodyGainsNoUnsubscribeField(t *testing.T) {
	e := newTestEnv(t, "ulnofield")
	user := e.seedUser(t, "ulnofield@example.com")
	item := e.seedJobItemLink(t, user, "ulnofield@example.com", ptr(unsubProbeLink+"/platform"))

	r := e.do(t, "POST", "/v1/run", map[string]any{"job_item_id": item, "user_id": user, "unsubscribe_url": "https://evil.example.test/smuggled"})
	if r.status != 400 || r.errorCode() != "validation_failed" {
		t.Fatalf("a run request carrying `unsubscribe_url`: status=%d code=%q, want 400 validation_failed (the field does not exist; the link rides on the job_item): %s", r.status, r.errorCode(), r.raw)
	}
	if n := e.jobsInCampaign(t, user); n != 0 {
		t.Fatalf("the refused run created %d job row(s)", n)
	}
	// control: the same body without the field is accepted, and the job carries the PLATFORM's link, not the smuggled one
	ok := e.runItem(t, user, item).requireStatus(t, 202)
	if got := e.jobUnsubscribeURL(t, ok.str("job_id")); got == nil || *got != unsubProbeLink+"/platform" {
		t.Fatalf("control: the accepted run stored %v, want the job_item's unsubscribe_url", got)
	}
}

func TestRun_AnUnsendableLinkIsRefusedAndWritesNothing(t *testing.T) {
	e := newTestEnv(t, "ulbad")
	user := e.seedUser(t, "ulbad@example.com")

	// CONTROL first: exactly at the limit (the scheme's 8 plus 1993) is accepted and stored whole.
	atLimit := "https://" + strings.Repeat("a", 1993)
	okItem := e.seedJobItemLink(t, user, "atlimit@example.com", &atLimit)
	ok := e.runItem(t, user, okItem).requireStatus(t, 202)
	if got := e.jobUnsubscribeURL(t, ok.str("job_id")); got == nil || len(*got) != 2001 {
		t.Fatalf("control: the at-limit link was not stored whole")
	}

	bad := map[string]string{
		"an http link":        "http://unsubscribe.example.test/u/abc",
		"a scheme alone":      "https://",
		"a space":             unsubProbeLink + "/a b",
		"a TAB":               unsubProbeLink + "/a\tb",
		"a LF and a header":   unsubProbeLink + "\nBcc: victim@example.com",
		"a CRLF and a header": unsubProbeLink + "\r\nBcc: victim@example.com",
		"a bare CR":           unsubProbeLink + "\rX",
		"a control byte":      unsubProbeLink + "/\x01",
		"DEL":                 unsubProbeLink + "/\x7f",
		"a '<'":               unsubProbeLink + "/a<b",
		"a '>'":               unsubProbeLink + "/a>b",
		"2002 characters":     "https://" + strings.Repeat("a", 1994),
	}
	n := 0
	for name, link := range bad {
		n++
		l := link
		item := e.seedJobItemLink(t, user, "ulbad"+strings.Repeat("y", n)+"@example.com", &l)
		r := e.runItem(t, user, item)
		if r.status != 400 || r.errorCode() != "validation_failed" {
			t.Fatalf("%s: status=%d code=%q, want 400 validation_failed; body=%s", name, r.status, r.errorCode(), r.raw)
		}
		var rows int
		if err := e.admin.QueryRow(bgctx(), `SELECT count(*) FROM send_byo.jobs WHERE job_item_id=$1`, item).Scan(&rows); err != nil {
			t.Fatalf("count jobs: %v", err)
		}
		if rows != 0 {
			t.Fatalf("%s: the refused run left %d job row(s) for the item, want 0", name, rows)
		}
		// the refusal is the value, not the item: fix the link and the SAME item runs
		if _, err := e.admin.Exec(bgctx(), `UPDATE public.job_items SET unsubscribe_url=$2 WHERE id=$1`, item, unsubProbeLink); err != nil {
			t.Fatalf("%s: fix the link: %v", name, err)
		}
		e.runItem(t, user, item).requireStatus(t, 202)
	}
}

func TestDescribe_AdvertisesListUnsubscribe(t *testing.T) {
	e := newTestEnv(t, "uldescribe")
	r := e.do(t, "GET", "/v1/describe", nil).requireStatus(t, 200)
	caps, _ := r.body["capabilities"].(map[string]any)
	if caps["list_unsubscribe"] != true {
		t.Fatalf("capabilities.list_unsubscribe = %v, want true; capabilities=%v", caps["list_unsubscribe"], caps)
	}
	// the capabilities that were there are unchanged (additive-only)
	for key, want := range map[string]any{
		"single_send": true, "sequences": false, "reply_to_per_campaign": true,
		"open_tracking": false, "click_tracking": false, "content_variation": true,
		"plain_text_alternative": true, "pause_resume": true,
	} {
		if caps[key] != want {
			t.Fatalf("capabilities.%s = %v, want %v (an existing capability changed)", key, caps[key], want)
		}
	}
}
