package dispatch

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// buildHeaders constructs the §8 minimum guaranteed header set. The Control owns Message-ID and
// Date generation, so send_logs.headers and the bytes the worker sends are the SAME set (§8.1). The
// worker adds only transport-mandatory MIME framing; it never invents or reorders these (§8.1).
//
//	From       "Name" <addr>, or bare <addr> when from_name is NULL (P3-5: address-only).
//	To         recipient (single in Phase 3).
//	Subject    rendered subject (frozen at job creation).
//	Reply-To   omitted when the job has no reply_to (nullable, §8.1).
//	Message-ID <messageID> (Control-generated; doubles as the §4.3 idempotency fallback id).
//	Date       RFC 5322 send time.
//
// messageID must already be angle-bracketed "<...>". sentAt is the send-time timestamp.
func buildHeaders(fromName, fromAddress, recipient, subject, replyTo, messageID string, sentAt time.Time) map[string]string {
	h := map[string]string{
		"From":       formatFrom(fromName, fromAddress),
		"To":         recipient,
		"Subject":    subject,
		"Message-ID": messageID,
		"Date":       sentAt.Format(time.RFC1123Z), // RFC 5322-compatible date
	}
	if strings.TrimSpace(replyTo) != "" {
		h["Reply-To"] = replyTo
	}
	return h
}

// formatFrom renders the From header: quoted display-name + angle-addr when a name is present,
// bare angle-addr otherwise (P3-5 — no platform-wide default display name).
func formatFrom(fromName, fromAddress string) string {
	if strings.TrimSpace(fromName) == "" {
		return "<" + fromAddress + ">"
	}
	// Quote the display name and escape any embedded quotes/backslashes (RFC 5322 quoted-string).
	escaped := strings.ReplaceAll(fromName, "\\", "\\\\")
	escaped = strings.ReplaceAll(escaped, "\"", "\\\"")
	return fmt.Sprintf("%q <%s>", escaped, fromAddress)
}

// messageIDFor builds the Control-generated Message-ID "<uuid@sending-domain>" (§8.1). The
// sending-domain is derived from from_address; the uuid source is the job id (Phase 8 P8-5
// coordinated refinement — a deterministic-from-job_id id STRENGTHENS the idempotency backstop:
// a crash-after-250 resend of the SAME job looks like the SAME message, and distinct jobs still
// get distinct unique ids). Phase 3 adopts the job-id source now since it costs nothing and never
// weakens idempotency.
func messageIDFor(jobID, fromAddress string) string {
	return "<" + jobID + "@" + sendingDomain(fromAddress) + ">"
}

// sendingDomain extracts the domain from an email address (the part after the last '@').
func sendingDomain(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 && i+1 < len(addr) {
		return addr[i+1:]
	}
	return "localhost"
}

// hasCRLF reports whether s contains a CR or LF (or a NUL). These are the SMTP header/body
// separators; a value carrying one could inject extra headers (e.g. a Bcc) or split into the body
// when composed into the DATA payload. The Control owns the header set, so it rejects such a value
// here rather than emit poisoned wire bytes (defense-in-depth even though Phase 3 content is
// first-party platform content).
func hasCRLF(s string) bool {
	return strings.ContainsAny(s, "\r\n\x00")
}

// validateNoInjection checks every header-bound value + the envelope recipient/from for CR/LF/NUL.
// Returns the offending field name (or "" if clean) so the caller can requeue with a slug rather
// than transmit a header/body-injection payload (§8.1; SMTP header-injection guard).
func validateNoInjection(fromName, fromAddress, recipient, subject, replyTo string) string {
	switch {
	case hasCRLF(fromName):
		return "from_name"
	case hasCRLF(fromAddress):
		return "from_address"
	case hasCRLF(recipient):
		return "recipient"
	case hasCRLF(subject):
		return "subject"
	case hasCRLF(replyTo):
		return "reply_to"
	}
	return ""
}

// The one-click unsubscribe pair (Phase 5 S4). Mailbox providers require both headers of a bulk sender: List-Unsubscribe
// (RFC 2369 section 3.2: the URL in angle brackets) and List-Unsubscribe-Post (RFC 8058 section 3.1: exactly this value,
// which tells the provider it may POST to that URL to unsubscribe the recipient without opening a page). This module sends
// through the USER'S OWN SMTP account, so the Control builds both from the job's frozen link (send_byo.jobs.unsubscribe_url,
// send_byo_0060) and hands them to the worker with the other Control-supplied headers; the worker only relays them.
const (
	headerListUnsubscribe     = "List-Unsubscribe"
	headerListUnsubscribePost = "List-Unsubscribe-Post"
	// listUnsubscribePostValue is the single value RFC 8058 defines; it is a protocol constant, not a tuning dial.
	listUnsubscribePostValue = "List-Unsubscribe=One-Click"
	// maxHeaderLineOctets is RFC 5322 section 2.1.1's hard limit on a line (CRLF excluded). This module does not fold
	// headers, and the URL inside the angle brackets cannot be folded anyway, so a link whose header line would exceed it
	// cannot be sent as a valid message and is refused rather than emitted. A protocol bound, not a tuning dial.
	maxHeaderLineOctets = 998
)

// The reasons listUnsubscribeHeaders refuses a link. Fixed slugs: they are INTERNAL (logged and stored as the requeue
// reason, never serialized to a client) and they never carry the URL, which is a per-recipient capability.
var (
	errUnsubscribeScheme  = errors.New("unsubscribe_url_not_https")
	errUnsubscribeEmpty   = errors.New("unsubscribe_url_empty_after_scheme")
	errUnsubscribeChar    = errors.New("unsubscribe_url_control_or_space")
	errUnsubscribeNonText = errors.New("unsubscribe_url_not_ascii")
	errUnsubscribeBracket = errors.New("unsubscribe_url_angle_bracket")
	errUnsubscribeLong    = errors.New("unsubscribe_url_header_line_too_long")
	errHeaderPresent      = errors.New("list_unsubscribe_header_already_present")
)

// listUnsubscribeHeaders is the HEADER-SAFE BUILDER of the pair. A nil link (the column is NULL: the platform wrote none)
// yields no headers and no error: the message is exactly what it always was. A non-nil link must be ONE https URL made
// only of printable ASCII (0x21 to 0x7E) with no `<` or `>`:
//
//   - no CR, LF, NUL, TAB or any other control character, and no space (anything below 0x21, and DEL): a CR or LF would end
//     the header line and let the rest of the value start another header (a Bcc, a second From), the classic injection. The
//     database CHECK (jobs_unsubscribe_url_check) already forbids all of these; this refuses them AGAIN, because a value
//     that reached the row some other way (a break-glass write, a future writer) must still never reach a socket;
//   - no `<` or `>`, which would close the angle brackets the value is wrapped in and smuggle a second URL into the header;
//   - no byte above 0x7E: a raw 8-bit byte in a header needs SMTPUTF8, which this module does not negotiate, and a URI is
//     ASCII by definition (RFC 3986), so an internationalised link must arrive already percent- or punycode-encoded;
//   - the whole header line within RFC 5322's 998 octets (maxHeaderLineOctets).
//
// It is stricter than the database CHECK on purpose, and an EMPTY non-nil link is refused (NULL is "none", the empty string
// is a bad value). On success the two headers are returned, the URL wrapped in angle brackets exactly as RFC 2369 writes it.
// The error is a fixed slug that names the rule and never echoes the link.
func listUnsubscribeHeaders(link *string) (map[string]string, error) {
	if link == nil {
		return nil, nil
	}
	u := *link
	const scheme = "https://"
	if !strings.HasPrefix(u, scheme) {
		return nil, errUnsubscribeScheme
	}
	if len(u) == len(scheme) {
		return nil, errUnsubscribeEmpty
	}
	for i := 0; i < len(u); i++ {
		switch c := u[i]; {
		case c <= 0x20 || c == 0x7f:
			return nil, errUnsubscribeChar
		case c >= 0x80:
			return nil, errUnsubscribeNonText
		case c == '<' || c == '>':
			return nil, errUnsubscribeBracket
		}
	}
	value := "<" + u + ">"
	if len(headerListUnsubscribe)+len(": ")+len(value) > maxHeaderLineOctets {
		return nil, errUnsubscribeLong
	}
	return map[string]string{
		headerListUnsubscribe:     value,
		headerListUnsubscribePost: listUnsubscribePostValue,
	}, nil
}

// addHeaders merges extra into h in place, and REFUSES (errHeaderPresent, h left untouched) when h already carries any of
// extra's names under any capitalisation: header names are case-insensitive on the wire, so a "list-unsubscribe" already in
// the map plus ours would put the header twice in the message, which a provider reads as no valid one-click link at all
// (RFC 8058 section 3.1 speaks of one List-Unsubscribe and one List-Unsubscribe-Post header field per message). Nothing in buildHeaders can produce such a name today; the refusal is
// what keeps "exactly once" a property of this code and not of its current neighbours.
func addHeaders(h, extra map[string]string) error {
	for name := range extra {
		for have := range h {
			if strings.EqualFold(name, have) {
				return errHeaderPresent
			}
		}
	}
	for name, value := range extra {
		h[name] = value
	}
	return nil
}

// sendText is the plain-text alternative that goes on the wire (workerproto.Send.Text) for a claimed job:
// the job's rendered_text (frozen at run from the platform's public.job_items.rendered_text), or "" when the
// message has none. NULL, the empty string and a string with no non-whitespace character are ONE meaning,
// "no text part" (Phase 3 close-out C5: a new field cannot be ambiguous), and "" is omitted from the frame
// (`text,omitempty`), so such a message is built by the worker exactly as it always was, byte for byte.
// Anything else is returned VERBATIM: no trimming, no re-wrapping, no normalisation here (the worker owns the
// MIME rendering). services/worker/src/assemble.js textPartOf applies the same rule on its side.
func sendText(rendered *string) string {
	if rendered == nil || strings.TrimSpace(*rendered) == "" {
		return ""
	}
	return *rendered
}
