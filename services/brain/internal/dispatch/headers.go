package dispatch

import (
	"fmt"
	"strings"
	"time"
)

// buildHeaders constructs the §8 minimum guaranteed header set. The Brain owns Message-ID and
// Date generation, so send_logs.headers and the bytes the box sends are the SAME set (§8.1). The
// box adds only transport-mandatory MIME framing; it never invents or reorders these (§8.1).
//
//	From       "Name" <addr>, or bare <addr> when from_name is NULL (P3-5: address-only).
//	To         recipient (single in Phase 3).
//	Subject    rendered subject (frozen at job creation).
//	Reply-To   omitted when the job has no reply_to (nullable, §8.1).
//	Message-ID <messageID> (Brain-generated; doubles as the §4.3 idempotency fallback id).
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

// messageIDFor builds the Brain-generated Message-ID "<uuid@sending-domain>" (§8.1). The
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
