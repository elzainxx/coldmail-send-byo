package adminapi

import (
	"net/http"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// Authenticator gates /v1/admin/* with an operator bearer key (§4.2). V1 has exactly one scope,
// platform_admin (§4.3): any valid key grants it. Keys are stored HASHED (bcrypt, same discipline
// as sender.boxes.token_hash) in the admin service's env/secret manager — NO per-principal DB
// table in V1 (OD-9 dropped). Comparison is constant-time via bcrypt.
type Authenticator struct {
	// keyHashes are bcrypt hashes of the accepted admin keys. Empty => fail-closed (reject all),
	// mirroring the boxlink registry's fail-closed posture.
	keyHashes []string
}

// NewAuthenticator builds an Authenticator from a set of bcrypt key-hashes. Blank entries are
// dropped. An empty result fails closed.
func NewAuthenticator(keyHashes []string) *Authenticator {
	var hs []string
	for _, h := range keyHashes {
		if strings.TrimSpace(h) != "" {
			hs = append(hs, h)
		}
	}
	return &Authenticator{keyHashes: hs}
}

// authenticate reports whether the presented bearer key matches any configured hash. Fail-closed:
// no configured hashes, or a missing/blank key, rejects. bcrypt.CompareHashAndPassword is
// constant-time with respect to the correct key.
func (a *Authenticator) authenticate(bearer string) bool {
	if len(a.keyHashes) == 0 || bearer == "" {
		return false
	}
	ok := false
	// Check every hash (do not early-return on first match) so timing does not reveal which key
	// index matched. bcrypt itself is constant-time against the stored hash.
	for _, h := range a.keyHashes {
		if bcrypt.CompareHashAndPassword([]byte(h), []byte(bearer)) == nil {
			ok = true
		}
	}
	return ok
}

// bearerToken extracts the token from an "Authorization: Bearer <key>" header (case-insensitive
// scheme). Returns "" if absent/malformed.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	const prefix = "bearer "
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// requireAuth wraps a handler with the operator-key gate. On failure it writes a clean
// 401 unauthenticated envelope (no internal reason) and logs the rejection against the
// request_id. The admin key is NEVER logged.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rid := newRequestID()
		if !s.auth.authenticate(bearerToken(r)) {
			s.logReject(rid, r, "admin_key_invalid_or_missing")
			writeError(w, rid, newErr(codeUnauthenticated, "admin_key_invalid_or_missing"))
			return
		}
		// Stash the request id for the handler chain (so one request = one id in logs + envelope).
		next(w, r.WithContext(withRequestID(r.Context(), rid)))
	}
}
