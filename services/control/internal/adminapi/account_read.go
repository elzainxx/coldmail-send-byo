package adminapi

import (
	"net/http"
	"strconv"
	"strings"
)

// handleAccountGet: GET /v1/admin/accounts/{id} (§2.3). No secret, ever.
func (s *Server) handleAccountGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isUUID(id) {
		s.fail(w, r, newErr(codeNotFound, "account_id_malformed"))
		return
	}
	ctx, cancel := dbCtx(r.Context())
	defer cancel()
	a, err := getAccount(ctx, s.db, id)
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

// handleAccountList: GET /v1/admin/accounts (§2.3). Filters ?paused=, ?quarantined=, ?disabled=,
// ?user_id=. Operator sees ALL accounts. Cursor pagination on (created_at, id). Every item is the
// §2.1 object — no secret.
func (s *Server) handleAccountList(w http.ResponseWriter, r *http.Request) {
	limit := parseLimit(r)
	cur, cerr := parseCursor(r)
	if cerr != nil {
		s.fail(w, r, cerr)
		return
	}

	var conds []string
	var args []any
	add := func(cond string, val any) {
		args = append(args, val)
		conds = append(conds, strings.Replace(cond, "?", "$"+strconv.Itoa(len(args)), 1))
	}
	q := r.URL.Query()
	if v := q.Get("user_id"); v != "" {
		if !isUUID(v) {
			s.fail(w, r, newErr(codeValidationFailed, "user_id_filter_malformed").
				withDetails(fieldDetail{Field: "user_id", Issue: issueMalformed}))
			return
		}
		add("a.user_id = ?", v)
	}
	for _, bf := range []struct{ q, col string }{{"paused", "a.paused"}, {"quarantined", "a.quarantined"}, {"disabled", "a.disabled"}} {
		if v := q.Get(bf.q); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				s.fail(w, r, newErr(codeValidationFailed, bf.q+"_filter_malformed").
					withDetails(fieldDetail{Field: bf.q, Issue: issueMalformed}))
				return
			}
			add(bf.col+" = ?", b)
		}
	}
	if cur != nil {
		args = append(args, cur.CreatedAt)
		p1 := "$" + strconv.Itoa(len(args))
		args = append(args, cur.ID)
		p2 := "$" + strconv.Itoa(len(args))
		conds = append(conds, "(a.created_at, a.id) > ("+p1+"::timestamptz, "+p2+"::uuid)")
	}

	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, limit+1)
	limitParam := "$" + strconv.Itoa(len(args))
	query := "SELECT " + accountSelectCols + ", " + cursorKeyColAccounts + " FROM send_byo.accounts a" + where +
		" ORDER BY a.created_at ASC, a.id ASC LIMIT " + limitParam

	ctx, cancel := dbCtx(r.Context())
	defer cancel()
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "account_list_query_error: "+err.Error()))
		return
	}
	defer rows.Close()

	items := []accountResource{}
	keys := []string{} // keys[i] is items[i]'s MICROSECOND cursor key (pagination.go: never the displayed CreatedAt)
	for rows.Next() {
		var key string
		a, serr := scanAccount(keyedRow{Rows: rows, key: &key})
		if serr != nil {
			s.fail(w, r, newErr(codeInternalError, "account_list_scan_error: "+serr.Error()))
			return
		}
		items = append(items, *a)
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		s.fail(w, r, newErr(codeInternalError, "account_list_rows_error: "+err.Error()))
		return
	}

	// limit+1 lookahead: the extra row only says "there is another page". The cursor anchors on the
	// LAST ROW KEPT (index limit-1) at full precision, so the next page starts strictly after it.
	var nextCursor *string
	if len(items) > limit {
		c := encodeCursor(keys[limit-1], items[limit-1].ID)
		items = items[:limit]
		nextCursor = &c
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": nextCursor})
}
