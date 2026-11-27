package dispatch

// list_unsubscribe_test.go — Phase 5 S4, the Control half of the one-click unsubscribe pair: what a job's frozen link
// (send_byo.jobs.unsubscribe_url, send_byo_0060) becomes on the worker wire.
//
//   - listUnsubscribeHeaders (pure): a NULL link is no headers; a link is exactly `List-Unsubscribe: <URL>` and
//     `List-Unsubscribe-Post: List-Unsubscribe=One-Click`; and the HEADER-SAFE BUILDER refuses every byte a header cannot
//     carry (CR, LF, NUL, every control character, space, `<`, `>`, anything non-ASCII), a non-https value, an empty
//     value and a line over RFC 5322's 998 octets, with an error that never echoes the link. The byte test is
//     exhaustive (all 256 byte values) and its paired control is the accepted half of the same table;
//   - addHeaders (pure): each name ends up in the frame exactly once, and a name already present under ANY
//     capitalisation is refused with the map left untouched;
//   - both claim queries RETURN the link, so it reaches the Control;
//   - the push_job frame carries both headers, exactly once each, for a job with a link and NEITHER for a job without
//     one, in either order (no header state leaks from one job to the next);
//   - a link that is not header-safe (forced past the database CHECK, which the test drops on its own clone) is never
//     pushed, spends no slot of the account's daily limit, and is requeued with a fixed slug that does not carry the URL;
//   - send_logs.headers records the pair for the job that had a link and nothing for the one that had none.
//
// The paired control of every "absent" case is the "present" case in the same test (and the reverse).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/coldmail-send-byo/control/internal/workerproto"
)

const listProbeBase = "https://unsubscribe.example.test/u/AbC-123_xyz"

// fixedSentAt is a send time for the pure tests (buildHeaders formats it into Date; nothing here reads the clock).
func fixedSentAt() time.Time { return time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC) }

func TestListUnsubscribeHeaders_NullIsNoHeaders_ALinkIsExactlyTheTwoHeaders(t *testing.T) {
	// the absent twin: NULL is no headers and no error
	if h, err := listUnsubscribeHeaders(nil); err != nil || h != nil {
		t.Fatalf("a NULL link must be no headers and no error, got %v / %v", h, err)
	}
	// the present case: exactly two headers, the URL in angle brackets, the one RFC 8058 value
	link := listProbeBase
	h, err := listUnsubscribeHeaders(&link)
	if err != nil {
		t.Fatalf("a valid link was refused: %v", err)
	}
	if len(h) != 2 || h["List-Unsubscribe"] != "<"+listProbeBase+">" || h["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" {
		t.Fatalf("headers = %v, want exactly List-Unsubscribe: <URL> and List-Unsubscribe-Post: List-Unsubscribe=One-Click", h)
	}
	// a query, a fragment and a percent escape are legal URL text and pass verbatim
	rich := listProbeBase + "?a=b&c=%20#frag"
	if h, err := listUnsubscribeHeaders(&rich); err != nil || h["List-Unsubscribe"] != "<"+rich+">" {
		t.Fatalf("a link with a query, a fragment and a %%20 was changed or refused: %v / %v", h, err)
	}
}

// Every byte value, placed inside an otherwise valid link. The expectation is written from the RULE (printable ASCII, no angle
// bracket), not read back from the implementation, so a loosened builder fails on the first byte it should have refused, and the
// accepted bytes are the control that the builder is not simply refusing everything.
func TestListUnsubscribeHeaders_EveryByteValue(t *testing.T) {
	accepted, refused := 0, 0
	for b := 0; b < 256; b++ {
		link := listProbeBase + "/" + string([]byte{byte(b)}) + "x"
		wantOK := b >= 0x21 && b <= 0x7e && b != '<' && b != '>'
		h, err := listUnsubscribeHeaders(&link)
		if wantOK {
			accepted++
			if err != nil || h["List-Unsubscribe"] != "<"+link+">" {
				t.Errorf("byte 0x%02x is header-safe and was refused or changed: %v / %v", b, h, err)
			}
			continue
		}
		refused++
		if err == nil || h != nil {
			t.Errorf("byte 0x%02x (%q) is NOT header-safe and was ACCEPTED: %v", b, string([]byte{byte(b)}), h)
		}
	}
	// anti-vacuity: the table really has both halves (94 printable ASCII bytes minus '<' and '>' = 92 accepted)
	if accepted != 92 || refused != 256-92 {
		t.Fatalf("byte table: %d accepted, %d refused, want 92 and %d", accepted, refused, 256-92)
	}
}

func TestListUnsubscribeHeaders_HostileValuesAreRefused(t *testing.T) {
	hostile := map[string]string{
		"CRLF and a Bcc":                    listProbeBase + "\r\nBcc: victim@example.com",
		"LF and a Bcc":                      listProbeBase + "\nBcc: victim@example.com",
		"a bare CR":                         listProbeBase + "\rX-Injected: 1",
		"a NUL":                             listProbeBase + "/\x00",
		"a TAB":                             listProbeBase + "/a\tb",
		"a trailing CRLF":                   listProbeBase + "\r\n",
		"a leading CRLF":                    "\r\n" + listProbeBase,
		"a space":                           listProbeBase + "/a b",
		"a closing bracket and a 2nd URL":   listProbeBase + ">, <https://evil.example.test/",
		"an opening bracket":                "<" + listProbeBase,
		"a mailto scheme":                   "mailto:unsubscribe@example.test",
		"an http scheme":                    "http://unsubscribe.example.test/u/abc",
		"an uppercase scheme":               "HTTPS://unsubscribe.example.test/u/abc",
		"a javascript scheme":               "javascript:alert(1)",
		"a scheme-relative URL":             "//unsubscribe.example.test/u/abc",
		"a scheme alone":                    "https://",
		"the empty string (not NULL)":       "",
		"a non-ASCII host":                  "https://unsubscribe.exämple.test/u/abc",
		"a Unicode line separator (U+2028)": listProbeBase + "/\u2028x",
		"a C1 NEL (U+0085)":                 listProbeBase + "/\u0085x",
	}
	for name, v := range hostile {
		link := v
		h, err := listUnsubscribeHeaders(&link)
		if err == nil || h != nil {
			t.Errorf("%s: ACCEPTED %q -> %v", name, v, h)
			continue
		}
		// the error is a fixed slug that never echoes the link (a per-recipient capability; it is logged and stored as the requeue reason)
		if strings.Contains(err.Error(), "example.test") || strings.Contains(err.Error(), "Bcc") || strings.ContainsAny(err.Error(), "\r\n<>") {
			t.Errorf("%s: the error carries the link or a control character: %q", name, err.Error())
		}
	}
	// the control the whole table is measured against
	good := listProbeBase
	if _, err := listUnsubscribeHeaders(&good); err != nil {
		t.Fatalf("control: the base link itself is refused: %v", err)
	}
}

// RFC 5322 section 2.1.1: a line is at most 998 octets, CRLF excluded. The boundary is computed from the rule, not from the code.
func TestListUnsubscribeHeaders_TheHeaderLineStaysWithinRFC5322(t *testing.T) {
	fixed := len("List-Unsubscribe: <") + len(">") + len("https://")
	atLimit := "https://" + strings.Repeat("a", 998-fixed)
	if got := len("List-Unsubscribe: <" + atLimit + ">"); got != 998 {
		t.Fatalf("test arithmetic: the at-limit line is %d octets, want 998", got)
	}
	if h, err := listUnsubscribeHeaders(&atLimit); err != nil || h == nil {
		t.Fatalf("a link whose header line is exactly 998 octets was refused: %v", err)
	}
	over := atLimit + "a"
	if h, err := listUnsubscribeHeaders(&over); err == nil || h != nil {
		t.Fatalf("a link whose header line is 999 octets was ACCEPTED (an invalid message line): %v", h)
	}
}

func TestAddHeaders_EachNameExactlyOnce_AndADuplicateNameIsRefused(t *testing.T) {
	link := listProbeBase
	pair, err := listUnsubscribeHeaders(&link)
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	// the normal case: merged, each name present once, the existing headers untouched
	h := buildHeaders("Sender", "sender@ourdomain.com", "rcpt@example.com", "Subject", "", "<id@ourdomain.com>", fixedSentAt())
	before := len(h)
	if err := addHeaders(h, pair); err != nil {
		t.Fatalf("addHeaders: %v", err)
	}
	if len(h) != before+2 {
		t.Fatalf("the map holds %d headers, want %d (the existing %d plus the two)", len(h), before+2, before)
	}
	for _, name := range []string{"List-Unsubscribe", "List-Unsubscribe-Post"} {
		n := 0
		for have := range h {
			if strings.EqualFold(have, name) {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("header %s is present %d times in the frame, want exactly once", name, n)
		}
	}
	// a nil pair (a job without a link) changes nothing, and the base set has neither header
	h2 := buildHeaders("Sender", "sender@ourdomain.com", "rcpt@example.com", "Subject", "", "<id@ourdomain.com>", fixedSentAt())
	if err := addHeaders(h2, nil); err != nil || len(h2) != before {
		t.Fatalf("addHeaders(nil) changed the map or failed: len=%d err=%v", len(h2), err)
	}
	for name := range h2 {
		if strings.HasPrefix(strings.ToLower(name), "list-unsubscribe") {
			t.Fatalf("buildHeaders alone emitted %s: only a job WITH a link may carry it", name)
		}
	}
	// a name already present, whatever its capitalisation, is refused and the map is left exactly as it was
	for _, existing := range []string{"List-Unsubscribe", "list-unsubscribe", "LIST-UNSUBSCRIBE-POST", "List-unsubscribe-post"} {
		m := map[string]string{"From": "<a@b.example>", existing: "<https://other.example.test/x>"}
		err := addHeaders(m, pair)
		if err == nil {
			t.Fatalf("addHeaders accepted a map that already had %q: the header would appear twice", existing)
		}
		if len(m) != 2 || m[existing] != "<https://other.example.test/x>" {
			t.Fatalf("a refused addHeaders changed the map: %v", m)
		}
	}
}

// --- Postgres-backed ------------------------------------------------------------------------------------------------

func (e *dispatchEnv) setUnsubscribeURL(t *testing.T, jobID string, link *string) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(), `UPDATE send_byo.jobs SET unsubscribe_url=$2 WHERE id=$1`, jobID, link); err != nil {
		t.Fatalf("set unsubscribe_url: %v", err)
	}
}

// Both claim queries must RETURN unsubscribe_url: a claim that did not would hand the dispatcher a job without its link and every
// message would silently go out without the headers (the column/destination count mismatch of C5 shows up here too).
func TestListUnsubscribe_BothClaimQueriesReturnTheLink(t *testing.T) {
	e := newDispatchEnv(t, "unsubclaim")
	ctx := context.Background()

	withLink := e.seedQueuedJob(t, "claim-link@example.com")
	e.setUnsubscribeURL(t, withLink, strptr(listProbeBase+"/single"))
	j, ok, err := e.d.claimOne(ctx, e.px.workerID, 120)
	if err != nil || !ok {
		t.Fatalf("claimOne: ok=%v err=%v", ok, err)
	}
	if j.id != withLink || j.unsubscribeURL == nil || *j.unsubscribeURL != listProbeBase+"/single" {
		t.Fatalf("claimOne returned job %s unsubscribeURL=%v, want job %s with its link", j.id, j.unsubscribeURL, withLink)
	}

	user := e.seedUser(t, "claim-link-tenant@example.com")
	userJob := e.seedQueuedJobFor(t, user, "claim-link-2@example.com")
	e.setUnsubscribeURL(t, userJob, strptr(listProbeBase+"/per-user"))
	noLink := e.seedQueuedJobFor(t, user, "claim-link-3@example.com")
	got, err := e.d.claimForUser(ctx, user, 10, 120)
	if err != nil || len(got) != 2 {
		t.Fatalf("claimForUser: %d jobs err=%v, want 2", len(got), err)
	}
	for _, g := range got {
		switch g.id {
		case userJob:
			if g.unsubscribeURL == nil || *g.unsubscribeURL != listProbeBase+"/per-user" {
				t.Fatalf("claimForUser dropped unsubscribe_url for %s: %v", g.id, g.unsubscribeURL)
			}
		case noLink:
			if g.unsubscribeURL != nil {
				t.Fatalf("claimForUser invented a link for the job that has none: %q", *g.unsubscribeURL)
			}
		default:
			t.Fatalf("claimForUser returned an unexpected job %s", g.id)
		}
	}
}

// The frame: both headers exactly once for a job with a link, neither for a job without, in BOTH orders (the with-link job first,
// then the without, and the reverse), so a header map shared or reused between jobs is caught. The serialized frame is what the
// worker receives, so the count is taken on the JSON, not on the Go map.
func TestListUnsubscribe_PushJobCarriesTheTwoHeadersOnlyWhenThereIsALink(t *testing.T) {
	e := newDispatchEnv(t, "unsubpush")
	ctx := context.Background()
	e.seedAccount(t, "secret-for-unsub-test")

	type tc struct {
		name string
		link *string
	}
	order := []tc{
		{"link first", strptr(listProbeBase + "/one")},
		{"no link after a link", nil},
		{"no link again", nil},
		{"link after no link", strptr(listProbeBase + "/two")},
	}
	for i, c := range order {
		job := e.seedQueuedJob(t, fmt.Sprintf("unsubpush%d@example.com", i))
		e.setUnsubscribeURL(t, job, c.link)
		pushed, err := e.d.TryDispatch(ctx)
		if err != nil || pushed != job {
			t.Fatalf("%s: TryDispatch pushed %q err=%v, want %s", c.name, pushed, err, job)
		}
		pj, ok := e.px.lastPush()
		if !ok {
			t.Fatalf("%s: no push recorded", c.name)
		}
		raw, err := json.Marshal(pj.Send)
		if err != nil {
			t.Fatalf("%s: marshal send: %v", c.name, err)
		}
		nUnsub := strings.Count(string(raw), `"List-Unsubscribe":`)
		nPost := strings.Count(string(raw), `"List-Unsubscribe-Post":`)
		if c.link != nil {
			if nUnsub != 1 || nPost != 1 || pj.Send.Headers["List-Unsubscribe"] != "<"+*c.link+">" || pj.Send.Headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" {
				t.Fatalf("%s: frame headers = %v (List-Unsubscribe x%d, List-Unsubscribe-Post x%d), want each exactly once with the link in angle brackets", c.name, pj.Send.Headers, nUnsub, nPost)
			}
		} else if nUnsub != 0 || nPost != 0 {
			t.Fatalf("%s: a job WITHOUT a link carries List-Unsubscribe x%d / List-Unsubscribe-Post x%d: %s", c.name, nUnsub, nPost, raw)
		}
		// the rest of the frame is untouched: the §8 set is still all there, the body is the body, there is still no text part
		for _, name := range []string{"From", "To", "Subject", "Message-ID", "Date"} {
			if pj.Send.Headers[name] == "" {
				t.Fatalf("%s: the %s header is missing from the frame: %v", c.name, name, pj.Send.Headers)
			}
		}
		if pj.Send.Body != "<p>b</p>" || pj.Send.Text != "" {
			t.Fatalf("%s: the body or text part changed: body=%q text=%q", c.name, pj.Send.Body, pj.Send.Text)
		}
		e.oh.Handle(e.px.workerID, workerproto.Report{Type: workerproto.TypeReport, JobID: job, Outcome: workerproto.OutcomeAccepted,
			ProviderMessageID: fmt.Sprintf("PM-UNSUB-%d", i), SMTPResponse: "250 2.0.0 Ok"})
		e.px.freeAllWorkers()

		// the send_logs record matches the bytes sent: the pair for the job that had a link, nothing for the one that had none
		var hasUnsub, hasPost bool
		if err := e.admin.QueryRow(ctx, `SELECT headers ? 'List-Unsubscribe', headers ? 'List-Unsubscribe-Post' FROM send_byo.send_logs WHERE job_id=$1`, job).Scan(&hasUnsub, &hasPost); err != nil {
			t.Fatalf("%s: read send_logs.headers: %v", c.name, err)
		}
		if hasUnsub != (c.link != nil) || hasPost != (c.link != nil) {
			t.Fatalf("%s: send_logs.headers has List-Unsubscribe=%v List-Unsubscribe-Post=%v, want both %v", c.name, hasUnsub, hasPost, c.link != nil)
		}
	}
}

// A link that is not header-safe can only reach the row if the CHECK was bypassed, so the test drops the CHECK on its own clone
// to put one there. It must NEVER be pushed, it must not spend a slot of the account's daily limit (the guard runs before an
// account is selected), and it is requeued with a fixed slug that does not carry the URL. The control is a clean job in the same
// env: it pushes and does spend a slot, so a guard that refused everything, or one that fired after the spend, is red.
func TestListUnsubscribe_AHostileLinkIsRefusedBeforeAnythingIsSpentOrSent(t *testing.T) {
	e := newDispatchEnv(t, "unsubcrlf")
	ctx := context.Background()
	acct := e.seedAccount(t, "sec")
	if _, err := e.admin.Exec(ctx, `ALTER TABLE send_byo.jobs DROP CONSTRAINT jobs_unsubscribe_url_check`); err != nil {
		t.Fatalf("drop the CHECK on the clone (to force a hostile value past the database): %v", err)
	}

	secretToken := "SECRET-TOKEN-4471"
	job := e.seedQueuedJob(t, "unsubinj@example.com")
	e.setUnsubscribeURL(t, job, strptr("https://unsubscribe.example.test/u/"+secretToken+"\r\nBcc: victim@evil.example"))
	pushed, err := e.d.TryDispatch(ctx)
	if err != nil {
		t.Fatalf("TryDispatch: %v", err)
	}
	if pushed == job || e.px.pushCount() != 0 {
		t.Fatalf("a job with a CR/LF in its unsubscribe link was pushed (pushed=%q, pushes=%d): the header guard failed", pushed, e.px.pushCount())
	}
	state, _, leaseNull, _, attempt := e.jobState(t, job)
	if state != "queued" || !leaseNull || attempt != 1 {
		t.Fatalf("hostile-link job state=%s leaseNull=%v attempt=%d, want queued/true/1 (requeued)", state, leaseNull, attempt)
	}
	var daily int
	if err := e.admin.QueryRow(ctx, `SELECT daily_sent_count FROM send_byo.accounts WHERE id=$1`, acct).Scan(&daily); err != nil {
		t.Fatalf("read daily_sent_count: %v", err)
	}
	if daily != 0 {
		t.Fatalf("daily_sent_count=%d after a refused link, want 0 (the guard must run before the account is consumed)", daily)
	}
	var reason string
	if err := e.admin.QueryRow(ctx, `SELECT last_error_message FROM send_byo.jobs WHERE id=$1`, job).Scan(&reason); err != nil {
		t.Fatalf("read last_error_message: %v", err)
	}
	if !strings.HasPrefix(reason, "header_invalid:") || strings.Contains(reason, secretToken) || strings.Contains(reason, "Bcc") {
		t.Fatalf("requeue reason = %q, want a fixed header_invalid: slug that carries no part of the link", reason)
	}

	// control: remove the poisoned job; a clean job WITH a link pushes, carries the pair, and spends exactly one slot
	if _, err := e.admin.Exec(ctx, `DELETE FROM send_byo.jobs WHERE id=$1`, job); err != nil {
		t.Fatalf("cleanup poisoned job: %v", err)
	}
	clean := e.seedQueuedJob(t, "unsubclean@example.com")
	e.setUnsubscribeURL(t, clean, strptr(listProbeBase))
	if got, _ := e.d.TryDispatch(ctx); got != clean {
		t.Fatalf("clean job with a valid link not pushed (got %q, want %q) — the guard over-rejects", got, clean)
	}
	if err := e.admin.QueryRow(ctx, `SELECT daily_sent_count FROM send_byo.accounts WHERE id=$1`, acct).Scan(&daily); err != nil || daily != 1 {
		t.Fatalf("daily_sent_count=%d err=%v after the clean job, want 1", daily, err)
	}
}
