package platformapi

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// Authenticator gates Domain A with the PLATFORM SERVICE TOKEN (§6 / Rule 6). It is a DISTINCT
// credential from the operator admin key (Domain B) — the two never share. Tokens are stored
// HASHED (bcrypt, same discipline as workers.token_hash / admin keys); comparison is constant-time.
// Empty hash set => fail-closed (reject all), so the surface is off until a token is configured.
type Authenticator struct {
	keyHashes []string
}

// NewAuthenticator builds an Authenticator from bcrypt token-hashes. Blank entries dropped.
func NewAuthenticator(keyHashes []string) *Authenticator {
	var hs []string
	for _, h := range keyHashes {
		if strings.TrimSpace(h) != "" {
			hs = append(hs, h)
		}
	}
	return &Authenticator{keyHashes: hs}
}

// Enabled reports whether any token is configured (drives whether the surface mounts).
func (a *Authenticator) Enabled() bool { return len(a.keyHashes) > 0 }

// authenticate reports whether the presented bearer token matches any configured hash. Checks all
// hashes (no early return) so timing does not reveal which index matched.
func (a *Authenticator) authenticate(bearer string) bool {
	if len(a.keyHashes) == 0 || bearer == "" {
		return false
	}
	ok := false
	for _, h := range a.keyHashes {
		if bcrypt.CompareHashAndPassword([]byte(h), []byte(bearer)) == nil {
			ok = true
		}
	}
	return ok
}

// bearerToken extracts "Authorization: Bearer <token>" (case-insensitive scheme).
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

// requireAuth wraps a handler with the service-token gate + a panic recover (clean 500, never a
// bare reset). The token is NEVER logged.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rid := newRequestID()
		r = r.WithContext(withRequestID(r.Context(), rid))

		defer func() {
			if rec := recover(); rec != nil {
				logError(rid, r, newErr(codeInternalError, fmt.Sprintf("panic_recovered: %v", rec)))
				writeError(w, rid, newErr(codeInternalError, "panic_recovered"))
			}
		}()

		if !s.auth.authenticate(bearerToken(r)) {
			logReject(rid, r, "service_token_invalid_or_missing")
			writeError(w, rid, newErr(codeUnauthenticated, "service_token_invalid_or_missing"))
			return
		}
		next(w, r)
	}
}

// fail writes the clean envelope and logs the internal reason against rid.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, e *apiError) {
	rid := requestIDFrom(r.Context())
	logError(rid, r, e)
	writeError(w, rid, e)
}

// --- logging (internal reason only; never a credential/body) ---

func logReject(rid string, r *http.Request, reason string) {
	log.Printf("platformapi reject rid=%s %s %s: %s level=warn", rid, r.Method, sanitizePath(r.URL.Path), reason)
}
func logError(rid string, r *http.Request, e *apiError) {
	log.Printf("platformapi error rid=%s %s %s: code=%s reason=%s level=%s",
		rid, r.Method, sanitizePath(r.URL.Path), e.code, e.internalReason, logLevelFor(e.code))
}

// logLevelFor separates "the caller asked for something that isn't there" (level=warn, routine and
// expected) from "this side broke" (level=error, act on it). Both render identically otherwise, so
// without this an operator grepping logs during an incident cannot tell a 404 from a 500. Derived
// from codeStatus rather than a second list, so a new code cannot be added and forgotten here.
func logLevelFor(code string) string {
	if status, ok := codeStatus[code]; ok && status < http.StatusInternalServerError {
		return "warn"
	}
	return "error"
}

// sanitizePath keeps only the route shape (no query string, which could carry ids) and neutralizes
// CONTROL CHARACTERS, so one request can never write two log lines.
//
// The control-character mapping closes a live log-forgery defect, found while adding the mux-level
// 405/404 (item 7b) and fixed here rather than left for someone to notice. r.URL.Path is the DECODED
// path, so a %0A inside a matched wildcard segment arrives as a literal newline; every line above is
// a single Printf, so that newline SPLITS THE LINE and the remainder is indistinguishable from a
// genuine entry with an attacker-chosen request_id. Reproduced against the live stack:
//
//	curl "http://localhost:9080/v1/admin/config/x%0Aadmin%20request_id=req_FORGED%20outcome=ok" \
//	     -H "Authorization: Bearer nope"
//
// wrote a second, timestamp-less line reading `admin request_id=req_FORGED outcome=ok
// outcome=rejected reason=admin_key_invalid_or_missing level=warn`. The defect predates item 7b, but
// 7b WIDENS it: the new fallbacks log ARBITRARY unmatched paths, not only paths that matched a
// registered pattern.
//
// Every byte below 0x20 (CR, LF, TAB, NUL...) plus DEL becomes '?': a substitution, not a deletion,
// so the logged path still shows that something was there.
func sanitizePath(p string) string {
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, p)
}

// context helpers -----------------------------------------------------------------------------

type ctxKey int

const ridKey ctxKey = 0

func withRequestID(ctx context.Context, rid string) context.Context {
	return context.WithValue(ctx, ridKey, rid)
}
func requestIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(ridKey).(string); ok {
		return v
	}
	return ""
}
