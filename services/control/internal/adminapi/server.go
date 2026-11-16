package adminapi

import (
	"context"
	"net/http"
	"time"

	"github.com/coldmail-send-byo/control/internal/fromgate"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SystemUserID is the reserved platform/system user_id (§0.1, ruling 2): the all-zero UUID.
// gen_random_uuid() (UUID v4) can never produce it, so it never collides with a real
// public.users(id). The module has SELECT-only on public.users and CANNOT seed it — the platform
// must (a coordination item). Pre-G2 this was also the owning user_id of shared pools and
// accounts assigned to them; that use died with send_byo.pools (M3-P2-OD-6/§2.1.1) — it survives
// today only as the fixed id test fixtures and dev seeding use for "the platform/module's own
// account," not any end user's.
const SystemUserID = "00000000-0000-0000-0000-000000000000"

// AccountVerifier is the seam through which handleAccountCreate triggers a real worker verification
// (Module 3 Phase 3 C5) without adminapi importing internal/dispatch (OD-7's package-isolation
// precedent — the SAME shape as platformapi.AccountVerifier; *dispatch.Verifier satisfies both
// structurally via two separate adapter types in main.go, since the two interfaces cannot share a
// definition across the package boundary). An admin-created account must verify exactly like a
// user-created one: M3-P3-OD-4's eligibility term (credential_verification_state='verified')
// applies uniformly, with no special-casing by which surface created the row.
type AccountVerifier interface {
	Verify(ctx context.Context, accountID, userID string, operationTimeoutSec int) (VerifyResult, error)
}

// VerifyResult mirrors dispatch.VerifyResult's shape (duplicated per OD-7 isolation — the same
// discipline platformapi.VerifyResult already follows).
type VerifyResult struct {
	Verified     bool
	State        string
	ExternalCode string
	Message      string
	SMTPResponse string
}

// Server is the operator admin API. It holds the DB pool (as send_byo_app), the Encrypt-only
// credential Encryptor, the operator-key Authenticator, and (Module 3 Phase 3 C5) the
// AccountVerifier seam for verify-on-add. It registers its routes on a mux that the Control mounts
// under /v1/admin/*.
//
// handler is what Handler() returns: the mux WRAPPED in methodDispatch, so a method mismatch or an
// unmatched /v1/admin/* path leaves through the error envelope instead of ServeMux's plain text
// (fix-plan item 7b). It is built once at construction, not per request.
type Server struct {
	db            *pgxpool.Pool
	enc           *Encryptor
	auth          *Authenticator
	verifier      AccountVerifier
	fromGateCache *fromgate.Cache
	mux           *http.ServeMux
	handler       http.Handler
}

// NewServer wires the admin routes. db is the send_byo_app pool (never superuser); enc is the
// Encrypt-only credential primitive; auth is the operator-key gate. verifier may be nil in tests
// that don't exercise verify-on-add; handleAccountCreate treats a nil verifier as "verification
// unavailable" (logged, never surfaced as a create failure — the same §2.5 precedent
// platformapi's own create path already follows).
//
// fromGateCache (Phase 4 §6.5) is constructed ONCE here, mirroring platformapi's own
// NewServer — see that package's comment for why a live config change to either TTL needs a
// Control restart to take effect.
func NewServer(db *pgxpool.Pool, enc *Encryptor, auth *Authenticator, verifier AccountVerifier) *Server {
	s := &Server{db: db, enc: enc, auth: auth, verifier: verifier, mux: http.NewServeMux()}
	s.fromGateCache = s.newFromGateCacheFromConfig()
	s.routes()
	return s
}

// newFromGateCacheFromConfig mirrors platformapi's own function (duplicated per OD-7 isolation).
func (s *Server) newFromGateCacheFromConfig() *fromgate.Cache {
	positiveSec, negativeSec := 3600, 300 // send_byo_0039's seeded defaults
	if s.db != nil {
		ctx, cancel := dbCtx(context.Background())
		defer cancel()
		if n, err := readConfigInt(ctx, s.db, "from_dns_cache_ttl_sec"); err == nil {
			positiveSec = n
		}
		if n, err := readConfigInt(ctx, s.db, "from_dns_negative_cache_ttl_sec"); err == nil {
			negativeSec = n
		}
	}
	return fromgate.NewCache(time.Duration(positiveSec)*time.Second, time.Duration(negativeSec)*time.Second)
}

// Handler exposes the served handler so the Control can mount it (e.g. under a parent mux).
//
// This returns the WRAPPED handler, never s.mux directly: returning the bare mux would restore the
// plain-text 405/404 that item 7b exists to remove, and it would do so silently — every route would
// still work. That is why the wrapper is built in routes() and stored.
func (s *Server) Handler() http.Handler { return s.handler }

// route is one (method, pattern, handler) registration. adminRoutes is the SINGLE declared list
// routes() mounts from — Module 3 Phase 6 §4.1's T10 requirement ("enumerate the mux's registered
// routes... this is the test that makes the 19th route safe") needs one source of truth to
// enumerate, not a second hand-copied list that could itself drift from what routes() actually
// mounts. adminRoutesTest.go (test-only) iterates this exact slice.
type route struct {
	method  string
	pattern string
	handler http.HandlerFunc
}

// adminRoutes is every Phase 2+ route (method + path patterns, Go 1.22+ mux). Every route is
// mounted behind requireAuth (the operator-key gate) by routes() below — none bypasses it, since
// routes() has no other way to reach s.mux.HandleFunc. Additive-only: routes never rename; later
// phases add. The served set here must match Docs/api-contract.md (the contract_drift gate).
func (s *Server) adminRoutes() []route {
	return []route{
		// Accounts
		{"POST", "/v1/admin/accounts", s.handleAccountCreate},
		{"GET", "/v1/admin/accounts", s.handleAccountList},
		{"GET", "/v1/admin/accounts/{id}", s.handleAccountGet},
		{"PATCH", "/v1/admin/accounts/{id}", s.handleAccountUpdate},
		{"DELETE", "/v1/admin/accounts/{id}", s.handleAccountDelete},
		{"GET", "/v1/admin/accounts/{id}/events", s.handleAccountEvents},

		// Account lifecycle transitions (manual, audited)
		{"POST", "/v1/admin/accounts/{id}/quarantine", s.handleAccountQuarantine},
		{"POST", "/v1/admin/accounts/{id}/unquarantine", s.handleAccountUnquarantine},
		{"POST", "/v1/admin/accounts/{id}/disable", s.handleAccountDisable},
		{"POST", "/v1/admin/accounts/{id}/enable", s.handleAccountEnable},
		{"POST", "/v1/admin/accounts/{id}/pause", s.handleAccountPause},
		{"POST", "/v1/admin/accounts/{id}/unpause", s.handleAccountUnpause},

		// Pools — UNMOUNTED AT G2 (Docs/history/phase-2-strip-shared-pool.md §2.4, per the user's ruling on
		// M3-P2-OD-10: kept mounted through G1 only because ~150 HTTP-level fixture sites depended
		// on POST /v1/admin/pools until the account write-path rewrite landed. That rewrite landed
		// in the SAME commit, so the fixture dependency is gone and the five routes were removed
		// together with pool_handlers.go/pool_store.go/pool_list_update.go.

		// Config
		{"GET", "/v1/admin/config", s.handleConfigList},
		{"GET", "/v1/admin/config/{key}", s.handleConfigGet},
		{"PUT", "/v1/admin/config/{key}", s.handleConfigPut},

		// Observability (Phase 9.5 B3/B4, §3)
		{"GET", "/v1/admin/health", s.handleHealth},

		// Module 3 Phase 5 §6: per-user suspend — genuinely new operator surface, module 2 had no
		// per-user concept at all. "Users control their own assets; the operator controls the
		// shared substrate" — per-user suspension is explicitly named as an operator-only control.
		{"POST", "/v1/admin/users/{id}/suspend", s.handleUserSuspend},
		{"POST", "/v1/admin/users/{id}/unsuspend", s.handleUserUnsuspend},
		// The P2 close-out (owner ruling C2): the suspension READ route — the missing half of the two
		// POSTs above. Read-only, no body, not rate limited, no audit row (user_suspension_read.go).
		{"GET", "/v1/admin/users/{id}/suspension", s.handleUserSuspensionGet},

		// Module 3 Phase 6 §4: the per-user config override write path — admin-only, per §4.2's
		// structural boundary (a different table from the user-owned routing_strategy setting).
		{"GET", "/v1/admin/users/{id}/config", s.handleUserConfigOverrideGet},
		{"PUT", "/v1/admin/users/{id}/config", s.handleUserConfigOverridePut},
		{"DELETE", "/v1/admin/users/{id}/config/{dial}", s.handleUserConfigOverrideDelete},
	}
}

// routes mounts every entry of adminRoutes() on the mux, each wrapped in requireAuth (the operator-
// key gate) — no route reaches s.mux.HandleFunc any other way, so a route present in adminRoutes()
// is structurally guaranteed to be authenticated (§4.1's "per-handler decorator, not middleware"
// risk is closed by construction: there is exactly one call site that can register a route at all).
//
// The SAME slice also builds the shape mux behind Handler()'s method-dispatch wrapper (item 7b), so
// a route cannot be served without a shape entry — which would make a wrong verb on it answer 404
// instead of 405.
func (s *Server) routes() {
	rs := s.adminRoutes()
	for _, rt := range rs {
		s.mux.HandleFunc(rt.method+" "+rt.pattern, s.requireAuth(rt.handler))
	}
	shape, allow := buildShapeMux(rs)
	s.handler = s.methodDispatch(s.mux, shape, allow)
}

// fail writes the clean external envelope for e and logs the internal reason against rid. It is
// the single choke point every handler uses to end an error path (two-vocabulary rule).
func (s *Server) fail(w http.ResponseWriter, r *http.Request, e *apiError) {
	rid := requestIDFrom(r.Context())
	s.logError(rid, r, e)
	writeError(w, rid, e)
}

// dbCtx bounds a per-request DB operation so a slow/locked statement cannot hang a handler.
func dbCtx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 10*time.Second)
}
