// Package platformapi is the Control's platform-facing surface (Domain A): quote / run / report /
// describe (Phase 3 §6). It is the END-USER (platform) surface, distinct from the operator admin
// surface (Domain B, internal/adminapi). The two auth domains NEVER share a credential (CLAUDE.md
// Rule 6): Domain A authenticates with the PLATFORM SERVICE TOKEN over the private network;
// Domain B uses the operator platform_admin bearer key. This package holds neither the credential
// Decryptor nor the Encryptor — it only creates/reads jobs.
//
// Two-vocabulary rule (Rule 5): every response is a clean envelope; a failure carries only
// {code, message, request_id} — never SQL/SQLSTATE, internal reason, or the worker's error_reason.
//
// Per-tenant isolation is ENFORCED by this module (OD-25, RULED option b): every platform request
// carries an explicit tenant selector (`user_id`) — `run` in the body, `report` in the query —
// and every platform-facing query scopes `WHERE user_id = $tenant`. A job that exists but belongs
// to another tenant returns the SAME 404-style not-found as a non-existent job (never 403), so a
// caller cannot see, nor even confirm the existence of, another tenant's jobs. This is
// defense-in-depth: even a platform-side bug that sends the wrong `user_id` cannot cross tenant
// boundaries in the module. See `Docs/history/open-decisions.md` OD-25.
package platformapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/coldmail-send-byo/control/internal/fromgate"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Kicker is the hook run() calls to nudge the dispatcher after creating a job. It must return
// IMMEDIATELY (async kickoff — run never blocks on the send, §6.2). Satisfied by a function that
// signals the dispatcher's run loop.
type Kicker func()

// AccountVerifier is the seam through which the account-create/test-button handlers trigger a
// real worker verification (Module 3 Phase 3 §2.1) without platformapi importing internal/dispatch
// (OD-7's package-isolation precedent) — *dispatch.Verifier satisfies this structurally. Verify
// runs synchronously (create's own response carries the result, §2.5: failure persists the row,
// it does not roll it back) and requires a connected worker; a caller with none configured (nil) sees
// verification simply never attempted, which only matters to tests that don't wire a worker.
type AccountVerifier interface {
	Verify(ctx context.Context, accountID, userID string, operationTimeoutSec int) (VerifyResult, error)
}

// VerifyResult mirrors dispatch.VerifyResult's shape (duplicated per OD-7 isolation — the two
// types are kept byte-for-byte in sync by hand, the same discipline as the crypto/uuid/pgxDB
// copies elsewhere in this package).
type VerifyResult struct {
	Verified     bool
	State        string
	ExternalCode string
	Message      string
	SMTPResponse string
}

// TestSender is the seam through which the test-send handler triggers a real send FORCED to a
// specific account (§3.2, M3-P3-OD-16). main.go's testSenderAdapter wraps *dispatch.Dispatcher
// and loads dispatch.Config itself (config-not-code freshness stays sourced fresh per call,
// mirroring every other Dispatcher entry point) — platformapi cannot import internal/dispatch
// (OD-7 isolation), so it never sees dispatch.Config at all, only this narrow signature.
// ErrAccountIneligible/ErrNoWorkerAvailable are this package's own sentinel errors the adapter maps
// dispatch's onto, so the handler can distinguish them from a generic internal error without
// importing dispatch's error values either.
type TestSender interface {
	SendTest(ctx context.Context, userID, accountID, recipient string) (jobID string, err error)
}

// ErrAccountIneligible/ErrNoWorkerAvailable are the two distinguishable TestSender failure modes a
// handler must render differently (a clean 409/422-shaped refusal vs an operational error) — the
// adapter (main.go) maps dispatch.ErrAccountIneligibleForTestSend/ErrNoWorkerForTestSend onto these.
var (
	ErrAccountIneligible = fmt.Errorf("account not eligible for test-send")
	ErrNoWorkerAvailable = fmt.Errorf("no connected worker available for test-send")
)

// Server is the platform API. It holds the DB pool (send_byo_app), the service-token
// authenticator, an async dispatcher kick, and (Module 3 Phase 3) the account-surface's OWN
// Encrypt-only credential primitive — a separate instance from adminapi.Encryptor (OD-7
// isolation), sharing only the underlying KMS key material main.go loads once — plus the
// AccountVerifier seam for verify-on-add.
//
// handler is what Handler() returns: the mux WRAPPED in methodDispatch, so a method mismatch or an
// unmatched path leaves through the error envelope instead of ServeMux's plain text (fix-plan item
// 7b). It is built once at construction, not per request.
type Server struct {
	db            *pgxpool.Pool
	auth          *Authenticator
	kick          Kicker
	enc           *Encryptor
	verifier      AccountVerifier
	testSender    TestSender
	fromGateCache *fromgate.Cache
	mux           *http.ServeMux
	handler       http.Handler
}

// NewServer wires the Domain-A routes. kick may be nil (run still succeeds; the dispatcher's own
// loop will pick the job up) but is normally the dispatcher nudge. enc may be nil ONLY in tests
// that never exercise the account-create/update paths — production always supplies one (main.go).
// verifier may be nil in tests that don't exercise verify-on-add; handleAccountCreate treats a nil
// verifier as "verification unavailable" (a clean internal_error), never a silent skip. testSender
// may be nil in tests that don't exercise test-send.
//
// fromGateCache (Phase 4 §6.5) is constructed ONCE here with the seeded defaults
// (from_dns_cache_ttl_sec/from_dns_negative_cache_ttl_sec), not re-read per request — §6.5's cache
// is a Control-process-local singleton with no coherence problem to solve by re-reading; a LIVE
// config change to either TTL takes effect on the Control's next restart, the same "cold cache costs
// latency, never correctness" posture §6.5 already documents for a restart's cache flush.
func NewServer(db *pgxpool.Pool, auth *Authenticator, kick Kicker, enc *Encryptor, verifier AccountVerifier, testSender TestSender) *Server {
	s := &Server{db: db, auth: auth, kick: kick, enc: enc, verifier: verifier, testSender: testSender, mux: http.NewServeMux()}
	s.fromGateCache = s.newFromGateCacheFromConfig()
	s.routes()
	return s
}

// newFromGateCacheFromConfig reads the two cache TTLs once at construction, falling back to the
// seeded defaults on a read/parse failure (db may be nil in package tests that never construct a
// real pool — that path skips the read entirely rather than querying a nil pool).
func (s *Server) newFromGateCacheFromConfig() *fromgate.Cache {
	positiveSec, negativeSec := 3600, 300 // send_byo_0039's seeded defaults
	if s.db != nil {
		ctx, cancel := dbCtx(context.Background())
		defer cancel()
		if n, ok, err := s.readConfigInt(ctx, "from_dns_cache_ttl_sec"); err == nil && ok {
			positiveSec = n
		}
		if n, ok, err := s.readConfigInt(ctx, "from_dns_negative_cache_ttl_sec"); err == nil && ok {
			negativeSec = n
		}
	}
	return fromgate.NewCache(time.Duration(positiveSec)*time.Second, time.Duration(negativeSec)*time.Second)
}

// Handler exposes the served handler for mounting under /v1/* on the Control's HTTP server.
//
// This returns the WRAPPED handler, never s.mux directly: returning the bare mux would restore the
// plain-text 405/404 that item 7b exists to remove, and it would do so silently — every route
// would still work. That is why the wrapper is built in routes() and stored, rather than composed
// at each call site that mounts this surface.
func (s *Server) Handler() http.Handler { return s.handler }

// routes registers the platform routes (Go 1.22+ method+path patterns), each behind the
// service-token gate, and builds the method-dispatch wrapper from the SAME table (item 7b).
// Additive-only; the served set must match Docs/api-contract.md.
//
// Module 3 Phase 3 (§1.2) adds the user-owned account surface under the namespaced
// /v1/send-byo/accounts... path (M3-P3-OD-1: the prefix token is `send_byo`, matching the schema
// name). quarantine/unquarantine are DROPPED from this surface (§1.2) — Phase 5 owns the
// user-facing un-quarantine bypass; only its hook (§2.4's verification columns) ships here.
//
// platformRoutes() flattens the core four and describe_routes.go's extensionRoutes() into the ONE
// list this loop registers from — nothing reaches s.mux.HandleFunc except this loop, so a route is
// structurally guaranteed to be (a) authenticated and (b) present in the shape mux that decides
// 405-vs-404. The extension registry remains the single DECLARATION site (§5.1: a route cannot be
// served without a describe declaration, and a declaration cannot exist without the mux entry it
// names); platformRoutes() only projects it into what dispatch needs.
func (s *Server) routes() {
	rs := s.platformRoutes()
	for _, rt := range rs {
		s.mux.HandleFunc(rt.method+" "+rt.pattern, s.requireAuth(rt.handler))
	}
	shape, allow := buildShapeMux(rs)
	s.handler = s.methodDispatch(s.mux, shape, allow)
}

// dbCtx bounds a per-request DB operation.
func dbCtx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 10*time.Second)
}
