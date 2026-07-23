package dispatch

import "encoding/json"

// mustJSON marshals a header map for the send_logs.headers JSONB column. A header map of strings
// never fails to marshal; on the impossible error path it returns an empty object rather than
// panicking a report.
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// scrubSmtpResponse is the outcome handler's defense-in-depth guard (§5.5): the credential should
// never appear in an SMTP response (the box already redacts, §4.4), but a pathological server
// could echo it. Phase 3 has no per-request secret here (the Brain zeroed its copy at push), so
// this is a placeholder that returns the response unchanged — the real scrub happens on the box,
// and the no-secret-leak gate + S1 assert the whole chain is clean. Kept as a named choke point
// so a future need to redact has one place to do it.
func scrubSmtpResponse(resp string) string {
	return resp
}
