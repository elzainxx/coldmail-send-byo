package adminapi

import (
	"log"
	"net/http"
)

// Logging discipline (Rule 5/6, §2.6): the admin service NEVER logs request bodies. It logs only
// {request_id, method, path, outcome, internal_reason} — the internal reason is the rich side of
// the two-vocabulary rule (safe to log, never returned). Because bodies are never logged, the
// smtp_secret and the ciphertext bytes structurally cannot appear in a log line (tests T1/T3
// assert zero occurrences of the plaintext and the ciphertext in captured logs). request_id
// bridges a log line to the client's error envelope.

// logReject records an auth/authorization rejection. The admin key is never included.
func (s *Server) logReject(rid string, r *http.Request, internalReason string) {
	log.Printf("admin request_id=%s method=%s path=%s outcome=rejected reason=%s",
		rid, r.Method, sanitizePath(r.URL.Path), internalReason)
}

// logError records a handled error against the request_id (the internal reason is the rich,
// loggable side; it never reaches the client). No body, no field values.
func (s *Server) logError(rid string, r *http.Request, e *apiError) {
	log.Printf("admin request_id=%s method=%s path=%s outcome=error code=%s reason=%s",
		rid, r.Method, sanitizePath(r.URL.Path), e.code, e.internalReason)
}

// logOK records a successful operation. It logs the action + affected resource id only — never a
// credential, never a body.
func (s *Server) logOK(rid string, r *http.Request, detail string) {
	log.Printf("admin request_id=%s method=%s path=%s outcome=ok %s",
		rid, r.Method, sanitizePath(r.URL.Path), detail)
}

// sanitizePath strips any query string from the logged path (query params are not expected to
// carry secrets on this surface, but bodies/queries are never logged verbatim regardless).
func sanitizePath(p string) string {
	if i := indexByte(p, '?'); i >= 0 {
		return p[:i]
	}
	return p
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
