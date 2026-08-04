package platformapi

// Mux-level 405/404 through the error envelope (audit D17 / fix-plan item 7b).
//
// THE DEFECT. Every route here is registered as a Go 1.22 method+path pattern
// ("POST /v1/quote"). net/http.ServeMux answers a METHOD MISMATCH or an UNMATCHED path itself,
// before requireAuth or any handler runs, with:
//
//	HTTP/1.1 405 Method Not Allowed        HTTP/1.1 404 Not Found
//	Content-Type: text/plain; charset=utf-8
//	Method Not Allowed                     404 page not found
//
// No `code`. No `request_id`. Not JSON. A client that does `body.error.code` on every non-2xx —
// which the contract invites, since it states request_id is mandatory on EVERY error — nil-derefs
// on a wrong-verb call. Reproduced live on this module before the fix: OPTIONS /v1/quote and
// OPTIONS /v1/send-byo/accounts/{id} both returned the plain text above. No gate noticed, because
// contract_drift probes each route with its method-appropriate verb only.
//
// THE FIX, AND WHY IT IS SHAPED LIKE THIS. Two ways were considered:
//
//  1. Register method-less fallback patterns ("/v1/quote") alongside the real ones. This is what
//     the fix plan suggests first, and on THIS surface it is not merely awkward but IMPOSSIBLE —
//     ServeMux panics at registration. Run against this module's actual route set it says:
//
//     pattern "/v1/send-byo/accounts/bulk" conflicts with pattern
//     "GET /v1/send-byo/accounts/{id}": /v1/send-byo/accounts/bulk matches more methods than
//     GET /v1/send-byo/accounts/{id}, but has a more specific path pattern
//
//     A method-less literal cannot coexist with a method+wildcard sibling, and
//     accounts/bulk + accounts/{id} is exactly that pair. The module would not start.
//
//  2. Own method dispatch in a wrapper (this file). The wrapper asks the REAL mux whether the
//     request matches anything; if not, it asks a PARALLEL SHAPE MUX built from the same route
//     table with the methods stripped. A shape hit means "the path exists, the verb is wrong" —
//     405 with an Allow header. A shape miss means "no such path" — 404. Both go out through
//     writeError, so they are the same envelope every other failure uses.
//
// mux.Handler(r) alone CANNOT distinguish the two cases: it returns an empty pattern for a method
// mismatch AND for an unmatched path (verified). The second mux is what supplies the distinction.
//
// WHY IT DELEGATES TO real.ServeHTTP AND NOT TO THE HANDLER Handler(r) RETURNS. Calling the
// returned handler directly LOSES r.PathValue — the returned handler is not bound to the matched
// pattern's wildcards, so every {id} route would silently see an empty id. This surface has
// FOURTEEN {id} routes, so that trap would break most of it while every envelope-shape assertion
// still passed. Handler(r) is used ONLY as a match ORACLE here; the mux always does the serving.
//
// WHAT THIS DELIBERATELY DOES NOT CHANGE:
//
//   - Route existence is still disclosed to an unauthenticated caller (404 for an unknown path,
//     401 for a real one). That is pre-existing and INTENTIONAL — contract_drift's whole
//     additive-only assertion is "a mounted route answers 401, a missing one 404". A wrong verb on
//     a real path already disclosed the same thing via the plain-text 405. No new disclosure: only
//     the SHAPE of the answer changes. This is distinct from the cross-tenant rule (an account or
//     job belonging to another tenant is 404, never 403), which is about DATA, not routes.
//   - Redirect behaviour (`//v1/quote` -> 301) is untouched: the real mux matches those, so they
//     never reach the fallback paths.
//   - HEAD on a GET route still hits the real handler (ServeMux matches HEAD to GET patterns), so
//     the Allow header advertises HEAD wherever GET is served.

import (
	"net/http"
	"sort"
	"strings"
)

// route is one served platform route, flattened from the two declaration sites this surface has:
// the four core routes (§5.1: quote/run/report/describe are implicit and uniform across modules)
// and describe_routes.go's extensionRoutes(). BOTH the real mux and the shape mux are built from
// this one flattening, so a route cannot be served without a shape entry (which would make a wrong
// verb on it 404 instead of 405), and a shape entry cannot exist for a route that is not served
// (which would make a nonexistent path 405 instead of 404).
//
// The extension registry keeps its own richer type (extensionRoute carries describe's declaration
// fields); this is the narrow projection dispatch needs, not a replacement for it.
type route struct {
	method  string
	pattern string
	handler http.HandlerFunc
}

// platformRoutes is the full served set: the core four, then every extension route in registry
// order. Additive-only; the served set must match Docs/api-contract.md (the contract_drift gate).
func (s *Server) platformRoutes() []route {
	rs := []route{
		{"POST", "/v1/quote", s.handleQuote},
		{"POST", "/v1/run", s.handleRun},
		{"GET", "/v1/report", s.handleReport},
		{"GET", "/v1/describe", s.handleDescribe},
	}
	for _, rt := range s.extensionRoutes() {
		rs = append(rs, route{rt.Method, rt.Path, rt.Handler})
	}
	return rs
}

// buildShapeMux builds the method-stripped twin of the real mux and the per-path Allow value.
//
// Paths are DEDUPED: registering the same method-less pattern twice panics
// (`pattern "/a" conflicts with pattern "/a": /a matches the same requests as /a`), and this
// surface has three paths carrying several methods each (/v1/send-byo/accounts,
// /v1/send-byo/accounts/{id}, /v1/send-byo/settings). The Allow value is the sorted method set,
// plus HEAD wherever GET is served, because ServeMux answers HEAD from a GET pattern and a client
// reading Allow should not be told otherwise.
func buildShapeMux(routes []route) (*http.ServeMux, map[string]string) {
	methods := map[string][]string{}
	for _, rt := range routes {
		methods[rt.pattern] = append(methods[rt.pattern], rt.method)
	}
	shape := http.NewServeMux()
	allow := make(map[string]string, len(methods))
	for pattern, ms := range methods {
		if containsMethod(ms, http.MethodGet) && !containsMethod(ms, http.MethodHead) {
			ms = append(ms, http.MethodHead)
		}
		sort.Strings(ms)
		allow[pattern] = strings.Join(ms, ", ")
		// The shape handler is never invoked: the wrapper uses Handler(r) as a match oracle only.
		// It must still be non-nil — ServeMux panics on a nil handler.
		shape.HandleFunc(pattern, func(http.ResponseWriter, *http.Request) {})
	}
	return shape, allow
}

func containsMethod(ms []string, want string) bool {
	for _, m := range ms {
		if m == want {
			return true
		}
	}
	return false
}

// methodDispatch wraps the real mux so a method mismatch or an unmatched path leaves through the
// SAME envelope as every other error, with a request_id.
//
// The rid is minted HERE rather than reused from requireAuth, because neither of these paths ever
// reaches requireAuth — that is the entire defect. The internal reason is logged against the rid
// and never serialized, the same two-vocabulary discipline as every handler.
//
// This surface is mounted under a PREFIX (/v1/send-byo/ in httpapi's root mux), so unlike the core
// four literals an arbitrary unrouted path under that prefix reaches this wrapper rather than the
// root mux's own fallback. Both answer the same coded 404; the logged path is sanitized at the
// logging site for exactly that reason.
func (s *Server) methodDispatch(real *http.ServeMux, shape *http.ServeMux, allow map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A non-empty pattern means the real mux has a handler for this method+path (including a
		// redirect match and HEAD-on-GET). Serve it through the MUX so wildcards bind.
		if _, pattern := real.Handler(r); pattern != "" {
			real.ServeHTTP(w, r)
			return
		}
		rid := newRequestID()
		r = r.WithContext(withRequestID(r.Context(), rid))

		if _, pattern := shape.Handler(r); pattern != "" {
			// The path exists; the verb does not. RFC 9110 requires Allow on a 405, and a client
			// that got here needs to know which verb to use.
			if a := allow[pattern]; a != "" {
				w.Header().Set("Allow", a)
			}
			e := newErr(codeMethodNotAllowed, "mux_method_not_allowed: "+r.Method+" on "+pattern)
			logError(rid, r, e)
			writeError(w, rid, e)
			return
		}
		// No such path on this surface.
		e := newErr(codeNotFound, "mux_no_route: "+r.Method)
		logError(rid, r, e)
		writeError(w, rid, e)
	})
}
