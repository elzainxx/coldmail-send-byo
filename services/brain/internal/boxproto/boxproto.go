// Package boxproto defines the Phase-3 box↔Brain control-channel message shapes (§2.2–§2.4).
// All messages are JSON objects with a `type` discriminator and a `request_id` for correlation,
// exchanged as newline-delimited JSON frames over the held mTLS stream (§2.1: the specific wire
// framing is an implementation choice; the LOCKED requirements are mutual auth + encryption +
// Brain-initiated push on the held stream — the box ships no RPC dependency, so line-delimited
// JSON over crypto/tls is the dependency-free realization on both sides).
//
// Two-vocabulary rule (§2.6): nothing on this internal channel flows verbatim to a platform
// caller. error_reason is an INTERNAL slug (logged, mapped to error_class='unknown' in Phase 3);
// smtp.secret is the decrypted plaintext scoped to ONE job and is never logged/persisted.
package boxproto

// Message type discriminators (§2.2). Additive-only.
const (
	TypeRegister    = "register"     // box → Brain: authenticate on stream open (§3.1)
	TypeRegisterAck = "register_ack" // Brain → box: accept/reject registration
	TypeHeartbeat   = "heartbeat"    // box → Brain: liveness ping (§3.2)
	TypePushJob     = "push_job"     // Brain → box: one job + its decrypted credential (§2.3)
	TypeReport      = "report"       // box → Brain: outcome of one job (§2.4)
	TypeReportAck   = "report_ack"   // Brain → box: outcome DURABLY persisted (Phase 7 §1.3, P7-4)
)

// Outcome values (§2.4). Phase 3 does NOT classify an error taxonomy (Phase 5).
const (
	OutcomeAccepted = "accepted"
	OutcomeError    = "error"
	OutcomeBounced  = "bounced" // hard 550; the locked state machine already models sending→bounced
)

// SMTP security modes (§2.3). Derived by the Brain from the port, explicit so the box does not
// re-derive policy.
const (
	SecurityStartTLS    = "starttls"     // 587/25: STARTTLS required before AUTH
	SecurityImplicitTLS = "implicit_tls" // 465: TLS at connect
)

// Register is the box's first message on stream open (§3.1).
type Register struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	BoxID     string `json:"box_id"`
	Token     string `json:"token"` // authorization AUTHORITY (verified vs sender.boxes.token_hash)
	// Optional diagnostics merged into box_metadata (§3.5); never read by dispatch.
	BoxVersion string `json:"box_version,omitempty"`
	Hostname   string `json:"hostname,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
}

// RegisterAck is the Brain's accept/reject (§3.1). On reject the Brain closes the stream and the
// box learns only the clean code (two-vocabulary rule); no job/credential is ever pushed.
type RegisterAck struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Accepted  bool   `json:"accepted"`
	Code      string `json:"code,omitempty"` // e.g. "unauthenticated" on reject
	// OutcomeBufferTTLSec delivers the seeded box_outcome_buffer_ttl_sec (Phase 7 §1.3, P7-4) to the
	// DB-less box on accept: the box retains/replays unacked outcomes up to this age, then drops them
	// and falls back to Brain reclaim. Additive-only, omitempty so older boxes/tests still parse; the
	// box falls back to its bootstrap env default when it is 0/absent. Config-not-code: the box holds
	// no seeded value of its own — it consumes what the Brain reads from sender.config (the same
	// pattern as smtp_operation_timeout_sec on push_job).
	OutcomeBufferTTLSec int `json:"outcome_buffer_ttl_sec,omitempty"`
}

// Heartbeat refreshes last_heartbeat_at even when idle (§3.2).
type Heartbeat struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	BoxID     string `json:"box_id"`
}

// Send is the rendered message content the box transmits (§2.3, headers per §8).
type Send struct {
	Recipient   string            `json:"recipient"`
	FromAddress string            `json:"from_address"`
	FromName    string            `json:"from_name,omitempty"`
	ReplyTo     string            `json:"reply_to,omitempty"`
	Subject     string            `json:"subject"`
	Body        string            `json:"body"`
	Headers     map[string]string `json:"headers"` // the §8 minimum guaranteed set the Brain built
}

// SMTP carries the connection + the decrypted plaintext credential for ONE send (§2.3). secret
// is memory-only on both sides, never logged/persisted, and zeroed after the session (§3.3/§4.4).
type SMTP struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Secret   string `json:"secret"`   // DECRYPTED plaintext SMTP password — scoped to this job
	Security string `json:"security"` // "starttls" | "implicit_tls"
	// OperationTimeoutSec delivers the seeded smtp_operation_timeout_sec (Phase 6 §5, P6-11) to the
	// DB-less box: the per-operation SMTP timeout (connect/greeting/socket-read on MAIL/RCPT/DATA)
	// the box must apply. Additive-only, omitempty so older frames/tests still parse; the box falls
	// back to its bootstrap env default when it is 0/absent. Enforced Brain-side to stay <
	// job_max_processing_time_sec (config-load reject) so an account hang surfaces as a box-REPORTED
	// timeout, not an unscored Brain lease reclaim (§2.4, P6-10).
	OperationTimeoutSec int `json:"operation_timeout_sec,omitempty"`
}

// PushJob delivers one job + its credential down the held stream (§2.3). One push = one send.
type PushJob struct {
	Type           string `json:"type"`
	RequestID      string `json:"request_id"`
	JobID          string `json:"job_id"`
	Send           Send   `json:"send"`
	SMTP           SMTP   `json:"smtp"`
	LeaseExpiresAt string `json:"lease_expires_at"` // RFC3339; the box's own deadline (§2.3)
}

// Report is the box's outcome for one job (§2.4). provider_message_id present iff accepted.
// smtp_response is the raw final SMTP line (never the credential). error_reason is an internal
// slug on failure. NONE of this flows verbatim to a platform caller.
type Report struct {
	Type              string `json:"type"`
	RequestID         string `json:"request_id"`
	JobID             string `json:"job_id"`
	Outcome           string `json:"outcome"` // accepted | error | bounced
	ProviderMessageID string `json:"provider_message_id,omitempty"`
	SMTPResponse      string `json:"smtp_response,omitempty"`
	ErrorReason       string `json:"error_reason,omitempty"`
}

// ReportAck is the Brain's POST-PERSIST acknowledgement of a report (Phase 7 §1.3, P7-4). The Brain
// sends it ONLY AFTER the report's outcome is durably committed to the DB (or determined to be a
// clean idempotent no-op — already-persisted / not-in-flight, which is equally "the Brain owns this
// job's resolution, the box may drop it"). Until a box receives report_ack for a job, it RETAINS the
// outcome and replays it on (re)connection (bounded by box_outcome_buffer_ttl_sec). Carrying job_id
// (not just request_id) lets the box match the ack even across a reconnect where request_ids differ.
// Two-vocabulary: the ack carries only {job_id, request_id, accepted} — never internal reasons.
type ReportAck struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	JobID     string `json:"job_id"`
	Accepted  bool   `json:"accepted"` // true = durably persisted (or clean no-op) → box may drop the buffered outcome
}

// error_reason slugs (§2.4). Internal only; mapped to error_class='unknown' in Phase 3 (§5.4).
const (
	ReasonConnectRefused    = "connect_refused"
	ReasonAuthFailed        = "auth_failed"
	ReasonStartTLSUnavail   = "starttls_unavailable"
	ReasonDataRejected4xx   = "data_rejected_4xx"
	ReasonDataRejected5xx   = "data_rejected_5xx"
	ReasonLeaseExpired      = "lease_expired"
	ReasonTimeout           = "timeout"
	ReasonAcceptedWithoutID = "accepted_without_id" // §4.3 defensive backstop (should be impossible)
)
