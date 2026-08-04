package platformapi

import (
	"net/http"
	"strconv"
	"strings"
)

// handleAccountGet: GET /v1/send-byo/accounts/{id}?user_id=<uuid> (§1.2). TENANT-SCOPED: a row
// that exists but belongs to another tenant, or is soft-deleted, returns the SAME not-found as a
// genuinely nonexistent id — no existence disclosure (§0.4/§1.2, OD-25's pattern applied here).
func (s *Server) handleAccountGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	userID := r.URL.Query().Get("user_id")
	if e := requireTenantSelector(userID); e != nil {
		s.fail(w, r, e)
		return
	}
	if !isUUID(id) {
		s.fail(w, r, newErr(codeNotFound, "account_id_malformed"))
		return
	}
	ctx, cancel := dbCtx(r.Context())
	defer cancel()
	a, err := getAccount(ctx, s.db, id, userID)
	if err == errAccountNotFound {
		s.fail(w, r, newErr(codeNotFound, "account_not_found:"+id))
		return
	}
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "account_get_error: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// handleAccountList: GET /v1/send-byo/accounts?user_id=<uuid> (§1.2). user_id is MANDATORY and
// SERVER-ENFORCED — unlike module 2's admin list (where it was an optional caller-chosen
// filter), here it is always the WHERE scope, never omittable to see other tenants' rows.
func (s *Server) handleAccountList(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if e := requireTenantSelector(userID); e != nil {
		s.fail(w, r, e)
		return
	}
	limit := parseLimit(r)
	cur, cerr := parseAccountCursor(r)
	if cerr != nil {
		s.fail(w, r, cerr)
		return
	}

	conds := []string{"a.user_id = $1", "a.deleted_at IS NULL"}
	args := []any{userID}
	if cur != nil {
		args = append(args, cur.CreatedAt)
		p1 := "$" + strconv.Itoa(len(args))
		args = append(args, cur.ID)
		p2 := "$" + strconv.Itoa(len(args))
		conds = append(conds, "(a.created_at, a.id) > ("+p1+"::timestamptz, "+p2+"::uuid)")
	}
	args = append(args, limit+1)
	limitParam := "$" + strconv.Itoa(len(args))
	query := "SELECT " + accountSelectCols + " FROM send_byo.accounts a WHERE " +
		strings.Join(conds, " AND ") + " ORDER BY a.created_at ASC, a.id ASC LIMIT " + limitParam

	ctx, cancel := dbCtx(r.Context())
	defer cancel()
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "account_list_query_error: "+err.Error()))
		return
	}
	defer rows.Close()

	items := []accountResource{}
	for rows.Next() {
		a, serr := scanAccount(rows)
		if serr != nil {
			s.fail(w, r, newErr(codeInternalError, "account_list_scan_error: "+serr.Error()))
			return
		}
		items = append(items, *a)
	}
	if err := rows.Err(); err != nil {
		s.fail(w, r, newErr(codeInternalError, "account_list_rows_error: "+err.Error()))
		return
	}

	var nextCursor *string
	if len(items) > limit {
		last := items[limit-1]
		items = items[:limit]
		c := encodeAccountCursor(last.CreatedAt, last.ID)
		nextCursor = &c
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": nextCursor})
}

// requireTenantSelector validates the mandatory user_id query selector (§1.1: the platform
// passes identity; the module enforces it). Empty vs malformed reported distinctly, matching
// handlers.go's own run/report user_id checks — one consistent vocabulary across this surface.
func requireTenantSelector(userID string) *apiError {
	if userID == "" {
		return newErr(codeValidationFailed, "user_id_required").
			withDetails(fieldDetail{Field: "user_id", Issue: issueRequired})
	}
	if !uuidRe.MatchString(userID) {
		return newErr(codeValidationFailed, "user_id_malformed").
			withDetails(fieldDetail{Field: "user_id", Issue: issueMalformed})
	}
	return nil
}
