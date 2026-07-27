package adminapi

import (
	"encoding/json"
	"net/http"
)

// Config management (§6). Read-all, read-one, update-one. value_type + semantic validation
// happens before write (§6.3/§6.4). The JSONB {"v": …} envelope is unwrapped on read and
// re-wrapped on write by configStore. Only `value` is writable; keys are not operator-creatable
// (unknown key → 404). Config is global (operator-owned); no per-tenant config in V1.

// handleConfigList: GET /v1/admin/config → { "items": [ <config object>, … ] }.
func (s *Server) handleConfigList(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r.Context())
	defer cancel()
	rows, err := listConfig(ctx, s.db)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "config_list_query_error: "+err.Error()))
		return
	}
	if rows == nil {
		rows = []configRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}

// handleConfigGet: GET /v1/admin/config/{key} → config object or 404.
func (s *Server) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	ctx, cancel := dbCtx(r.Context())
	defer cancel()
	row, err := getConfig(ctx, s.db, key)
	if err == errConfigKeyNotFound {
		s.fail(w, r, newErr(codeNotFound, "config_key_not_found:"+key))
		return
	}
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "config_get_query_error: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, row)
}

type configPutReq struct {
	Value json.RawMessage `json:"value"`
}

// handleConfigPut: PUT /v1/admin/config/{key} — update one. value_type + semantic validation
// (§6.3/§6.4) before write; re-wrap to {"v": …}; unknown key → 404.
func (s *Server) handleConfigPut(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")

	var req configPutReq
	present, derr := decodeStrict(r, &req)
	if derr != nil {
		s.fail(w, r, derr)
		return
	}
	if !hasField(present, "value") {
		s.fail(w, r, newErr(codeValidationFailed, "config_value_required").
			withDetails(fieldDetail{Field: "value", Issue: issueRequired}))
		return
	}

	ctx, cancel := dbCtx(r.Context())
	defer cancel()

	// Load the row first (to know value_type + to 404 unknown keys before validating/writing).
	row, err := getConfig(ctx, s.db, key)
	if err == errConfigKeyNotFound {
		s.fail(w, r, newErr(codeNotFound, "config_key_not_found:"+key))
		return
	}
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "config_get_query_error: "+err.Error()))
		return
	}

	// value_type + semantic validation (returns a clean apiError on failure).
	if verr := validateConfigValue(key, row.ValueType, req.Value); verr != nil {
		s.fail(w, r, verr)
		return
	}

	// Config-ordering invariants (P9-OD-2, RULED-AND-CLOSED): validate the RESULTING pair against
	// the counterpart key's LIVE current value — not the submitted value in isolation — BEFORE the
	// write lands. No-op for the 51 of 53 keys with no ordering counterpart.
	if row.ValueType == "integer" {
		if n, ok := asInt(req.Value); ok {
			if operr := checkConfigOrderingOnWrite(ctx, s.db, key, int(n)); operr != nil {
				s.fail(w, r, operr)
				return
			}
		}
	}

	if err := updateConfigValue(ctx, s.db, key, req.Value); err != nil {
		if err == errConfigKeyNotFound { // lost a race with a delete (keys aren't deletable in V1, but be safe)
			s.fail(w, r, newErr(codeNotFound, "config_key_not_found:"+key))
			return
		}
		s.fail(w, r, newErr(codeInternalError, "config_update_error: "+err.Error()))
		return
	}

	updated, err := getConfig(ctx, s.db, key)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "config_reread_error: "+err.Error()))
		return
	}
	s.logOK(requestIDFrom(r.Context()), r, "config_updated key="+key)
	writeJSON(w, http.StatusOK, updated)
}
