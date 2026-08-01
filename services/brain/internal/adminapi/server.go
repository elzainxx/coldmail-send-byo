package adminapi

import (
	"context"
	"net/http"
	"time"

	"github.com/byosmtp-module/brain/internal/fromgate"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SystemUserID is the reserved platform/system user_id (§0.1, ruling 2): the all-zero UUID.
// gen_random_uuid() (UUID v4) can never produce it, so it never collides with a real
// public.users(id). The module has SELECT-only on public.users and CANNOT seed it — the platform
// must (a coordination item). Pre-G2 this was also the owning user_id of shared pools and
// accounts assigned to them; that use died with byosmtp.pools (M3-P2-OD-6/§2.1.1) — it survives
// today only as the fixed id test fixtures and dev seeding use for "the platform/module's own
// account," not any end user's.
const SystemUserID = "00000000-0000-0000-0000-000000000000"

// AccountVerifier is the seam through which handleAccountCreate triggers a real box verification
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

// Server is the operator admin API. It holds the DB pool (as byosmtp_module), the Encrypt-only
// credential Encryptor, the operator-key Authenticator, and (Module 3 Phase 3 C5) the
// AccountVerifier seam for verify-on-add. It registers its routes on a mux that the Brain mounts
// under /v1/admin/*.
type Server struct {
	db            *pgxpool.Pool
	enc           *Encryptor
	auth          *Authenticator
	verifier      AccountVerifier
	fromGateCache *fromgate.Cache
	mux           *http.ServeMux
}

// NewServer wires the admin routes. db is the byosmtp_module pool (never superuser); enc is the
// Encrypt-only credential primitive; auth is the operator-key gate. verifier may be nil in tests
// that don't exercise verify-on-add; handleAccountCreate treats a nil verifier as "verification
// unavailable" (logged, never surfaced as a create failure — the same §2.5 precedent
// platformapi's own create path already follows).
//
// fromGateCache (Phase 4 §6.5) is constructed ONCE here, mirroring platformapi's own
// NewServer — see that package's comment for why a live config change to either TTL needs a
// Brain restart to take effect.
func NewServer(db *pgxpool.Pool, enc *Encryptor, auth *Authenticator, verifier AccountVerifier) *Server {
	s := &Server{db: db, enc: enc, auth: auth, verifier: verifier, mux: http.NewServeMux()}
	s.fromGateCache = s.newFromGateCacheFromConfig()
	s.routes()
	return s
}

// newFromGateCacheFromConfig mirrors platformapi's own function (duplicated per OD-7 isolation).
func (s *Server) newFromGateCacheFromConfig() *fromgate.Cache {
	positiveSec, negativeSec := 3600, 300 // byosmtp_0039's seeded defaults
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

// Handler exposes the mux so the Brain can mount it (e.g. under a parent mux).
func (s *Server) Handler() http.Handler { return s.mux }

// routes registers every Phase 2 route (method + path patterns, Go 1.22+ mux). Every route is
// behind requireAuth (the operator-key gate). Additive-only: routes never rename; later phases
// add. The served set here must match Docs/api-contract.md (the contract_drift gate).
func (s *Server) routes() {
	// Accounts
	s.mux.HandleFunc("POST /v1/admin/accounts", s.requireAuth(s.handleAccountCreate))
	s.mux.HandleFunc("GET /v1/admin/accounts", s.requireAuth(s.handleAccountList))
	s.mux.HandleFunc("GET /v1/admin/accounts/{id}", s.requireAuth(s.handleAccountGet))
	s.mux.HandleFunc("PATCH /v1/admin/accounts/{id}", s.requireAuth(s.handleAccountUpdate))
	s.mux.HandleFunc("DELETE /v1/admin/accounts/{id}", s.requireAuth(s.handleAccountDelete))
	s.mux.HandleFunc("GET /v1/admin/accounts/{id}/events", s.requireAuth(s.handleAccountEvents))

	// Account lifecycle transitions (manual, audited)
	s.mux.HandleFunc("POST /v1/admin/accounts/{id}/quarantine", s.requireAuth(s.handleAccountQuarantine))
	s.mux.HandleFunc("POST /v1/admin/accounts/{id}/unquarantine", s.requireAuth(s.handleAccountUnquarantine))
	s.mux.HandleFunc("POST /v1/admin/accounts/{id}/disable", s.requireAuth(s.handleAccountDisable))
	s.mux.HandleFunc("POST /v1/admin/accounts/{id}/enable", s.requireAuth(s.handleAccountEnable))
	s.mux.HandleFunc("POST /v1/admin/accounts/{id}/pause", s.requireAuth(s.handleAccountPause))
	s.mux.HandleFunc("POST /v1/admin/accounts/{id}/unpause", s.requireAuth(s.handleAccountUnpause))

	// Pools — UNMOUNTED AT G2 (Docs/phase-2-strip-shared-pool.md §2.4, per the user's ruling on
	// M3-P2-OD-10: kept mounted through G1 only because ~150 HTTP-level fixture sites depended on
	// POST /v1/admin/pools until the account write-path rewrite landed. That rewrite is this
	// commit, so the fixture dependency is gone and the five routes are removed together with
	// pool_handlers.go/pool_store.go/pool_list_update.go (deleted in this same commit).

	// Config
	s.mux.HandleFunc("GET /v1/admin/config", s.requireAuth(s.handleConfigList))
	s.mux.HandleFunc("GET /v1/admin/config/{key}", s.requireAuth(s.handleConfigGet))
	s.mux.HandleFunc("PUT /v1/admin/config/{key}", s.requireAuth(s.handleConfigPut))

	// Observability (Phase 9.5 B3/B4, §3)
	s.mux.HandleFunc("GET /v1/admin/health", s.requireAuth(s.handleHealth))

	// Module 3 Phase 5 §6: per-user suspend — genuinely new operator surface, module 2 had no
	// per-user concept at all. "Users control their own assets; the operator controls the shared
	// substrate" — per-user suspension is explicitly named as an operator-only control.
	s.mux.HandleFunc("POST /v1/admin/users/{id}/suspend", s.requireAuth(s.handleUserSuspend))
	s.mux.HandleFunc("POST /v1/admin/users/{id}/unsuspend", s.requireAuth(s.handleUserUnsuspend))
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
