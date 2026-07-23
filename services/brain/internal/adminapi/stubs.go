package adminapi

import "net/http"

// TEMPORARY stubs so the package compiles at the core+config checkpoint. Each is replaced by a
// real implementation in the following chunks (pools, accounts, lifecycle, events). A stub
// returns 500 internal_error with an internal reason marking it unimplemented — it is NEVER a
// silent success, so a route reached before its real handler lands fails loudly.
//
//nolint:unused // replaced incrementally

func (s *Server) stubUnimplemented(w http.ResponseWriter, r *http.Request, name string) {
	s.fail(w, r, newErr(codeInternalError, "unimplemented_stub:"+name))
}

func (s *Server) handleAccountCreate(w http.ResponseWriter, r *http.Request) {
	s.stubUnimplemented(w, r, "account_create")
}
func (s *Server) handleAccountList(w http.ResponseWriter, r *http.Request) {
	s.stubUnimplemented(w, r, "account_list")
}
func (s *Server) handleAccountGet(w http.ResponseWriter, r *http.Request) {
	s.stubUnimplemented(w, r, "account_get")
}
func (s *Server) handleAccountUpdate(w http.ResponseWriter, r *http.Request) {
	s.stubUnimplemented(w, r, "account_update")
}
func (s *Server) handleAccountDelete(w http.ResponseWriter, r *http.Request) {
	s.stubUnimplemented(w, r, "account_delete")
}
func (s *Server) handleAccountEvents(w http.ResponseWriter, r *http.Request) {
	s.stubUnimplemented(w, r, "account_events")
}
func (s *Server) handleAccountQuarantine(w http.ResponseWriter, r *http.Request) {
	s.stubUnimplemented(w, r, "account_quarantine")
}
func (s *Server) handleAccountUnquarantine(w http.ResponseWriter, r *http.Request) {
	s.stubUnimplemented(w, r, "account_unquarantine")
}
func (s *Server) handleAccountDisable(w http.ResponseWriter, r *http.Request) {
	s.stubUnimplemented(w, r, "account_disable")
}
func (s *Server) handleAccountEnable(w http.ResponseWriter, r *http.Request) {
	s.stubUnimplemented(w, r, "account_enable")
}
func (s *Server) handleAccountPause(w http.ResponseWriter, r *http.Request) {
	s.stubUnimplemented(w, r, "account_pause")
}
func (s *Server) handleAccountUnpause(w http.ResponseWriter, r *http.Request) {
	s.stubUnimplemented(w, r, "account_unpause")
}
func (s *Server) handlePoolCreate(w http.ResponseWriter, r *http.Request) {
	s.stubUnimplemented(w, r, "pool_create")
}
func (s *Server) handlePoolList(w http.ResponseWriter, r *http.Request) {
	s.stubUnimplemented(w, r, "pool_list")
}
func (s *Server) handlePoolGet(w http.ResponseWriter, r *http.Request) {
	s.stubUnimplemented(w, r, "pool_get")
}
func (s *Server) handlePoolUpdate(w http.ResponseWriter, r *http.Request) {
	s.stubUnimplemented(w, r, "pool_update")
}
func (s *Server) handlePoolDelete(w http.ResponseWriter, r *http.Request) {
	s.stubUnimplemented(w, r, "pool_delete")
}
