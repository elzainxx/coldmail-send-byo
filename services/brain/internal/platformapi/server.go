// Package platformapi is the Brain's platform-facing surface (Domain A): quote / run / report /
// describe (Phase 3 §6). It is the END-USER (platform) surface, distinct from the operator admin
// surface (Domain B, internal/adminapi). The two auth domains NEVER share a credential (CLAUDE.md
// Rule 6): Domain A authenticates with the PLATFORM SERVICE TOKEN over the private network;
// Domain B uses the operator platform_admin bearer key. This package holds neither the credential
// Decryptor nor the Encryptor — it only creates/reads jobs.
//
// Two-vocabulary rule (Rule 5): every response is a clean envelope; a failure carries only
// {code, message, request_id} — never SQL/SQLSTATE, internal reason, or the box's error_reason.
//
// Per-tenant isolation (spec §6): the locked Domain-A request shapes carry NO tenant selector
// (run{job_item_id}, report ?job_id/?job_item_id) and Domain A uses a single shared platform
// service token, so this module CANNOT scope reads/writes by a request-borne principal. Phase 3
// relies on the platform (the trusted sole Domain-A caller) enforcing tenant boundaries + the
// unguessable job UUIDs; whether the module should additionally take a tenant selector and filter
// sender.jobs by it is an OPEN DECISION (OD-25, Docs/open-decisions.md) awaiting a user ruling.
// This surface therefore does NOT itself enforce user-A-can't-see-user-B; do not add a silent
// tenant filter without resolving OD-25 (it changes the locked Domain-A contract).
package platformapi

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Kicker is the hook run() calls to nudge the dispatcher after creating a job. It must return
// IMMEDIATELY (async kickoff — run never blocks on the send, §6.2). Satisfied by a function that
// signals the dispatcher's run loop.
type Kicker func()

// Server is the platform API. It holds the DB pool (sender_module), the service-token
// authenticator, and an async dispatcher kick.
type Server struct {
	db   *pgxpool.Pool
	auth *Authenticator
	kick Kicker
	mux  *http.ServeMux
}

// NewServer wires the Domain-A routes. kick may be nil (run still succeeds; the dispatcher's own
// loop will pick the job up) but is normally the dispatcher nudge.
func NewServer(db *pgxpool.Pool, auth *Authenticator, kick Kicker) *Server {
	s := &Server{db: db, auth: auth, kick: kick, mux: http.NewServeMux()}
	s.routes()
	return s
}

// Handler exposes the mux for mounting under /v1/* on the Brain's HTTP server.
func (s *Server) Handler() http.Handler { return s.mux }

// routes registers the four Phase-3 platform routes (Go 1.22+ method+path patterns), each behind
// the service-token gate. Additive-only; the served set must match Docs/api-contract.md.
func (s *Server) routes() {
	s.mux.HandleFunc("POST /v1/quote", s.requireAuth(s.handleQuote))
	s.mux.HandleFunc("POST /v1/run", s.requireAuth(s.handleRun))
	s.mux.HandleFunc("GET /v1/report", s.requireAuth(s.handleReport))
	s.mux.HandleFunc("GET /v1/describe", s.requireAuth(s.handleDescribe))
}

// dbCtx bounds a per-request DB operation.
func dbCtx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 10*time.Second)
}
