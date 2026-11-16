package adminapi

// pagination_exact_test.go — the P2 close-out, owner ruling C1: every keyset listing on the admin
// surface is walked page by page and must return EXACTLY the set of rows the table holds — no gap, no
// duplicate, nothing extra, in the documented (created_at, id) order.
//
// THE DEFECT (FW-M3-15, widened here). The admin account list and the account-events list built
// next_cursor from the row's DISPLAYED created_at, which is second-precision. A cursor earlier than
// the row it points at makes the keyset predicate `(created_at, id) > (cursor_ts, cursor_id)` re-match
// every row of that second at or before the boundary: duplicates on the next page, and — with a page
// smaller than the number of rows in the second — a page made entirely of rows the caller already
// has, so the cursor never advances and every row after that second is unreachable. FW-M3-15
// recorded it as "a duplicate, not a skip"; the non-advancing case is a skip.
//
// THE SHAPE. Rows are stamped so they all share ONE wall-clock second, come in pairs that share an
// EXACT instant (so the id tiebreak is on a page boundary for limit=3), and carry a non-zero
// microsecond part (so the displayed, second-precision value is strictly earlier than the stored one).
// One checker (pageViolations) judges every walk. The PAIRED CONTROL runs the OLD cursor construction
// (a test-local pager that builds the cursor from the displayed second-precision value, exactly as the
// handlers did before this fix) over the SAME rows through the SAME walker and checker, and requires a
// violation — so a green production walk cannot be a checker that accepts anything.
//
// FAIL-VERIFY (the fault proof, ci/faults/p2c/m3-pagination in the platform repository): feed
// encodeCursor the displayed CreatedAt again (revert account_read.go to `last.CreatedAt`) and
// TestPagination_AdminAccountsListWalksEveryRowExactlyOnce goes red with a non-terminating walk.

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"
)

// pageWalk is the outcome of paging one listing to the end (or to the cap).
type pageWalk struct {
	ids        []string // every id served, in the order served, duplicates included
	terminated bool     // false when the cursor never ended inside maxPages
	oversized  []string // pages that returned more rows than the limit
}

// walkPages pages a listing until next is "" or maxPages is reached. fetch returns one page's ids and
// the next cursor ("" when the response said null).
func walkPages(limit, maxPages int, fetch func(cursor string) (ids []string, next string)) pageWalk {
	var w pageWalk
	cursor := ""
	for page := 1; page <= maxPages; page++ {
		ids, next := fetch(cursor)
		if len(ids) > limit {
			w.oversized = append(w.oversized, fmt.Sprintf("page %d returned %d rows for limit=%d", page, len(ids), limit))
		}
		w.ids = append(w.ids, ids...)
		if next == "" {
			w.terminated = true
			return w
		}
		cursor = next
	}
	return w
}

// pageViolations is THE checker: it compares a walk with the rows the table holds, in order. An empty
// result means the walk is exactly want — each id once, none missing, none extra, same order.
func pageViolations(w pageWalk, want []string) []string {
	var v []string
	if !w.terminated {
		v = append(v, "the walk did not terminate (a cursor that never advances)")
	}
	v = append(v, w.oversized...)
	seen := map[string]int{}
	for _, id := range w.ids {
		seen[id]++
	}
	wantSet := map[string]bool{}
	for _, id := range want {
		wantSet[id] = true
	}
	var dups, extra, missing []string
	for id, n := range seen {
		if n > 1 {
			dups = append(dups, fmt.Sprintf("%s x%d", id, n))
		}
		if !wantSet[id] {
			extra = append(extra, id)
		}
	}
	for _, id := range want {
		if seen[id] == 0 {
			missing = append(missing, id)
		}
	}
	sort.Strings(dups)
	sort.Strings(extra)
	if len(dups) > 0 {
		v = append(v, "duplicated: "+strings.Join(dups, ", "))
	}
	if len(extra) > 0 {
		v = append(v, "not in the table: "+strings.Join(extra, ", "))
	}
	if len(missing) > 0 {
		v = append(v, "missing (a gap): "+strings.Join(missing, ", "))
	}
	if len(v) == 0 && strings.Join(w.ids, ",") != strings.Join(want, ",") {
		v = append(v, "same rows, wrong order: got "+strings.Join(w.ids, ",")+" want "+strings.Join(want, ","))
	}
	return v
}

// TestPagination_CheckerObjectsToGapDuplicateExtraOrderAndLoop is the pure control for the checker:
// each way a walk can be wrong must be reported, and the right walk must not be.
func TestPagination_CheckerObjectsToGapDuplicateExtraOrderAndLoop(t *testing.T) {
	want := []string{"a", "b", "c", "d"}
	ok := pageWalk{ids: []string{"a", "b", "c", "d"}, terminated: true}
	if v := pageViolations(ok, want); len(v) != 0 {
		t.Fatalf("control premise: the right walk was rejected: %v", v)
	}
	bad := map[string]pageWalk{
		"a gap (one row skipped)":      {ids: []string{"a", "b", "d"}, terminated: true},
		"a duplicate":                  {ids: []string{"a", "b", "b", "c", "d"}, terminated: true},
		"an extra row":                 {ids: []string{"a", "b", "c", "d", "z"}, terminated: true},
		"the wrong order":              {ids: []string{"a", "c", "b", "d"}, terminated: true},
		"a cursor that never advances": {ids: []string{"a", "b", "a", "b"}, terminated: false},
		"a page over the limit":        {ids: []string{"a", "b", "c", "d"}, terminated: true, oversized: []string{"page 1 returned 4 rows for limit=2"}},
		"nothing at all":               {ids: nil, terminated: true},
	}
	for name, w := range bad {
		if v := pageViolations(w, want); len(v) == 0 {
			t.Errorf("the checker ACCEPTED a wrong walk (%s): %+v", name, w)
		}
	}
}

// stampAccountsOneSecond gives userID's accounts controlled created_at values: pairs share an exact
// instant, successive pairs are 100ms apart, all inside 2026-01-01T12:00:00, every one with a
// non-zero microsecond part.
func (e *testEnv) stampAccountsOneSecond(t *testing.T, userID string) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE send_byo.accounts a
		    SET created_at = TIMESTAMPTZ '2026-01-01 12:00:00+00' + (s.rn / 2) * INTERVAL '100 milliseconds' + INTERVAL '123 microseconds'
		   FROM (SELECT id, row_number() OVER (ORDER BY id) - 1 AS rn FROM send_byo.accounts WHERE user_id = $1) s
		  WHERE a.id = s.id`, userID); err != nil {
		t.Fatalf("stamp accounts: %v", err)
	}
}

// idsInOrder returns the ids a query yields, in the order it yields them (the table's own truth).
func (e *testEnv) idsInOrder(t *testing.T, sql string, args ...any) []string {
	t.Helper()
	rows, err := e.admin.Query(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("query ids: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan id: %v", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// fetchPage reads one page of a list route and returns its ids and next cursor.
func (e *testEnv) fetchPage(t *testing.T, path string, limit int, cursor string) ([]string, string) {
	t.Helper()
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	full := fmt.Sprintf("%s%slimit=%d", path, sep, limit)
	if cursor != "" {
		full += "&cursor=" + url.QueryEscape(cursor)
	}
	r := e.do(t, "GET", full, nil).requireStatus(t, 200)
	items, ok := r.body["items"].([]any)
	if !ok {
		t.Fatalf("response has no items[]: %s", r.raw)
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			t.Fatalf("an item is not an object: %s", r.raw)
		}
		id, ok := m["id"].(string)
		if !ok {
			t.Fatalf("an item has no string id: %s", r.raw)
		}
		ids = append(ids, id)
	}
	next, _ := r.body["next_cursor"].(string)
	return ids, next
}

// seedOneSecondAccounts creates n accounts for a fresh user (the admin create route), stamps them into
// one wall-clock second, asserts the PREMISE (they really do display one second but are distinct,
// with ties), and returns the user and the table's own ordering of their ids.
func (e *testEnv) seedOneSecondAccounts(t *testing.T, label string, n int) (string, []string) {
	t.Helper()
	userID := e.seedUser(t, label+"@example.com")
	for i := 0; i < n; i++ {
		e.mkAccount(t, userID, "h.example.com", fmt.Sprintf("%s-%d@example.com", label, i))
	}
	e.stampAccountsOneSecond(t, userID)
	var seconds, instants, total int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(DISTINCT date_trunc('second', created_at)), count(DISTINCT created_at), count(*)
		   FROM send_byo.accounts WHERE user_id = $1`, userID).Scan(&seconds, &instants, &total); err != nil {
		t.Fatalf("premise query: %v", err)
	}
	if seconds != 1 || instants <= 1 || instants >= total {
		t.Fatalf("premise broken: %d accounts display %d distinct second(s) and have %d distinct instants — want ONE second, several instants, and at least one tie; "+
			"without that this test cannot tell a second-precision cursor from a correct one", total, seconds, instants)
	}
	want := e.idsInOrder(t, `SELECT id::text FROM send_byo.accounts WHERE user_id = $1 ORDER BY created_at, id`, userID)
	if len(want) != n {
		t.Fatalf("seeded %d accounts, the table holds %d", n, len(want))
	}
	return userID, want
}

// TestPagination_AdminAccountsListWalksEveryRowExactlyOnce pages GET /v1/admin/accounts at several
// limits (1 puts the tie on every boundary, 3 puts a tied pair across one) and requires each walk to
// be exactly the table's rows.
func TestPagination_AdminAccountsListWalksEveryRowExactlyOnce(t *testing.T) {
	e := newTestEnv(t, "pgadmacct")
	userID, want := e.seedOneSecondAccounts(t, "pgadmacct", 7)
	// A second user's accounts must never appear in this user's walk (the filter is part of the key set).
	e.mkAccount(t, e.seedUser(t, "pgadmacct-other@example.com"), "h.example.com", "other@example.com")

	for _, limit := range []int{1, 2, 3, 7, 50} {
		w := walkPages(limit, len(want)+3, func(cursor string) ([]string, string) {
			return e.fetchPage(t, "/v1/admin/accounts?user_id="+userID, limit, cursor)
		})
		if v := pageViolations(w, want); len(v) != 0 {
			t.Fatalf("GET /v1/admin/accounts limit=%d: %s\n  served: %v\n  table:  %v", limit, strings.Join(v, "; "), w.ids, want)
		}
	}
}

// seedOneSecondEvents inserts n account_events rows for one account, stamped into one wall-clock
// second in tied pairs, and returns the table's own ordering of ALL of that account's event ids
// (those plus the real `created` row the create route wrote).
func (e *testEnv) seedOneSecondEvents(t *testing.T, label string, n int) (accountID string, want []string) {
	t.Helper()
	userID := e.seedUser(t, label+"@example.com")
	accountID = e.mkAccount(t, userID, "h.example.com", label+"@example.com")
	if _, err := e.admin.Exec(context.Background(),
		`INSERT INTO send_byo.account_events (account_id, user_id, actor, action, reason, created_at)
		 SELECT $1::uuid, $2::uuid, 'user', 'verified', 'verify: success #' || g,
		        TIMESTAMPTZ '2026-01-01 12:00:00+00' + ((g - 1) / 2) * INTERVAL '100 milliseconds' + INTERVAL '123 microseconds'
		   FROM generate_series(1, $3::int) g`, accountID, userID, n); err != nil {
		t.Fatalf("seed events: %v", err)
	}
	var seconds, instants, total int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(DISTINCT date_trunc('second', created_at)), count(DISTINCT created_at), count(*)
		   FROM send_byo.account_events WHERE account_id = $1 AND reason LIKE 'verify: success #%'`, accountID).Scan(&seconds, &instants, &total); err != nil {
		t.Fatalf("premise query: %v", err)
	}
	if total != n || seconds != 1 || instants <= 1 || instants >= total {
		t.Fatalf("premise broken: %d seeded events display %d distinct second(s) and have %d distinct instants — want ONE second, several instants, at least one tie", total, seconds, instants)
	}
	want = e.idsInOrder(t, `SELECT id::text FROM send_byo.account_events WHERE account_id = $1 ORDER BY created_at, id`, accountID)
	if len(want) != n+1 {
		t.Fatalf("the account has %d events, want %d seeded + the real created row", len(want), n)
	}
	return accountID, want
}

// TestPagination_AdminAccountEventsWalkEveryRowExactlyOnce is the same walk over GET
// /v1/admin/accounts/{id}/events.
func TestPagination_AdminAccountEventsWalkEveryRowExactlyOnce(t *testing.T) {
	e := newTestEnv(t, "pgadmevents")
	accountID, want := e.seedOneSecondEvents(t, "pgadmevents", 7)

	for _, limit := range []int{1, 2, 3, 8, 50} {
		w := walkPages(limit, len(want)+3, func(cursor string) ([]string, string) {
			return e.fetchPage(t, "/v1/admin/accounts/"+accountID+"/events", limit, cursor)
		})
		if v := pageViolations(w, want); len(v) != 0 {
			t.Fatalf("GET /v1/admin/accounts/{id}/events limit=%d: %s\n  served: %v\n  table:  %v", limit, strings.Join(v, "; "), w.ids, want)
		}
	}
}

// oldSecondPrecisionPager reproduces the cursor construction these two routes shipped BEFORE the
// close-out: the next cursor is the displayed, second-precision created_at of the last row kept plus
// its id. It is test-local code, deliberately independent of the handlers, so the control below runs
// the OLD behavior over the SAME rows even though the handlers no longer have it.
//
// table/where select accounts or events; the cursor it hands back is "<second>|<id>".
func (e *testEnv) oldSecondPrecisionPager(t *testing.T, table, where string, whereArg string, limit int, cursor string) ([]string, string) {
	t.Helper()
	q := `SELECT id::text, to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"') FROM ` + table + ` WHERE ` + where
	args := []any{whereArg}
	if cursor != "" {
		parts := strings.SplitN(cursor, "|", 2)
		q += ` AND (created_at, id) > ($2::timestamptz, $3::uuid)`
		args = append(args, parts[0], parts[1])
	}
	q += fmt.Sprintf(` ORDER BY created_at ASC, id ASC LIMIT %d`, limit+1)
	rows, err := e.admin.Query(context.Background(), q, args...)
	if err != nil {
		t.Fatalf("old pager query: %v", err)
	}
	defer rows.Close()
	var ids, shown []string
	for rows.Next() {
		var id, disp string
		if err := rows.Scan(&id, &disp); err != nil {
			t.Fatalf("old pager scan: %v", err)
		}
		ids = append(ids, id)
		shown = append(shown, disp)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("old pager rows: %v", err)
	}
	if len(ids) > limit {
		next := shown[limit-1] + "|" + ids[limit-1] // the OLD key: the DISPLAYED, second-precision value
		return ids[:limit], next
	}
	return ids, ""
}

// TestPagination_ControlOldSecondPrecisionCursorIsDetected is the PAIRED CONTROL: the old cursor
// construction, run through the same walker and the same checker over the same rows, must be reported
// as a violation — and must show the failure FW-M3-15 understated (a walk that does not terminate),
// not merely a duplicate. If this control ever passes, the production walks above prove nothing.
func TestPagination_ControlOldSecondPrecisionCursorIsDetected(t *testing.T) {
	e := newTestEnv(t, "pgadmcontrol")
	userID, wantAccounts := e.seedOneSecondAccounts(t, "pgadmcontrol", 7)
	eventAccount, wantEvents := e.seedOneSecondEvents(t, "pgadmcontrolev", 7)

	for _, c := range []struct {
		name, table, where, arg string
		want                    []string
	}{
		{"accounts", "send_byo.accounts", "user_id = $1", userID, wantAccounts},
		{"events", "send_byo.account_events", "account_id = $1", eventAccount, wantEvents},
	} {
		// limit=3 over 7-8 rows in one second: the old cursor re-serves the same rows forever.
		w := walkPages(3, len(c.want)+3, func(cursor string) ([]string, string) {
			return e.oldSecondPrecisionPager(t, c.table, c.where, c.arg, 3, cursor)
		})
		v := pageViolations(w, c.want)
		if len(v) == 0 {
			t.Fatalf("CONTROL FAILED (%s): the OLD second-precision cursor construction walked exactly the table's rows — the checker cannot detect FW-M3-15, so the production walks prove nothing; served %v", c.name, w.ids)
		}
		if w.terminated {
			t.Fatalf("CONTROL (%s): the old construction was reported as a violation (%v) but it TERMINATED; the dense-second case must be a walk that never advances", c.name, v)
		}
	}
}
