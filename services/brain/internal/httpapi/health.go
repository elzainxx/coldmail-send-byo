// Package httpapi serves the Brain's health stub. Phase-0 scope only: a read-only endpoint
// that reports TRUE state (DB reachable, singleton lock held). The full operator health
// surface (Domain B auth, queue/box/account metrics) is Phase 2/9.5 — not built here.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/sender-module/brain/internal/db"
	"github.com/sender-module/brain/internal/singleton"
)

// HealthServer answers /healthz and /readyz, and (Phase 2) mounts the operator admin surface
// under /v1/admin/* when an admin handler is provided. Health remains unauthenticated (a liveness/
// readiness probe); the admin routes carry their own Domain-B operator-key auth internally.
type HealthServer struct {
	http  *http.Server
	db    *db.DB
	guard *singleton.Guard
}

// NewHealthServer wires the health routes and, if adminHandler is non-nil, mounts it under
// /v1/admin/. Passing nil keeps the surface health-only (Phase 0/1 behavior, or when the admin
// key is unconfigured — fail-closed).
func NewHealthServer(addr string, database *db.DB, guard *singleton.Guard, adminHandler http.Handler) *HealthServer {
	h := &HealthServer{db: database, guard: guard}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.handleHealthz)
	mux.HandleFunc("/readyz", h.handleReadyz)
	if adminHandler != nil {
		// The admin server owns every /v1/admin/* route (method+path patterns, own auth).
		mux.Handle("/v1/admin/", adminHandler)
	}
	h.http = &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return h
}

func (h *HealthServer) ListenAndServe() error            { return h.http.ListenAndServe() }
func (h *HealthServer) Shutdown(c context.Context) error { return h.http.Shutdown(c) }

// handleHealthz is liveness: the process is up.
func (h *HealthServer) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "at": time.Now().UTC()})
}

// handleReadyz is readiness: DB reachable AND singleton lock genuinely held. Reports the
// real state (not a hardcoded ok) so the harness can assert against it.
func (h *HealthServer) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	dbOK := h.db != nil && h.db.Pool.Ping(ctx) == nil
	lockOK := h.guard != nil && h.guard.Healthy(ctx)

	code := http.StatusOK
	if !dbOK || !lockOK {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]any{
		"status":         statusWord(dbOK && lockOK),
		"db":             dbOK,
		"singleton_lock": lockOK,
		"at":             time.Now().UTC(),
	})
}

func statusWord(ok bool) string {
	if ok {
		return "ready"
	}
	return "not_ready"
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
