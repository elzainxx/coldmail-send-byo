package platformapi

// pagination_exact_test.go — the P2 close-out, owner ruling C1: every keyset listing on the platform
// surface is walked page by page and must return EXACTLY the set of rows the table holds — no gap, no
// duplicate, nothing extra, in the documented (created_at, id) order.
//
// TWO DEFECTS, ONE CHECKER.
//
//  1. The campaign report (GET /v1/report?campaign_id=) built next_cursor from the LOOKAHEAD row, not
//     the last row served. campaignItems scans pageSize+1 rows, and a single `last` variable
//     overwritten on every iteration ends holding the extra one; the next page starts strictly after
//     it, so that row is never returned: one item skipped per page boundary (M2-G5, found in module 2
//     and inherited by this fork).
//  2. accounts.list and accounts.events built next_cursor from the DISPLAYED created_at, which is
//     second-precision (FW-M3-15). A cursor earlier than the row it points at re-matches every row of
//     that second at or before the boundary: duplicates on the next page, and — with a page smaller
//     than the number of rows in the second — a page made entirely of rows the caller already has, so
//     the cursor never advances and every row after that second is unreachable.
//
// (notifications.list is the third keyset listing here and already builds its cursor from a
// microsecond key and the last row kept; notifications_read_test.go covers it. This file does not
// repeat it.)
//
// THE PAIRED CONTROLS run each OLD construction — test-local pagers, independent of the handlers —
// over the SAME rows through the SAME walker and checker and require a violation of the right KIND (a
// gap for the lookahead defect; a walk that never terminates for the second-precision one). A green
// production walk therefore cannot be a checker that accepts anything.
//
// FAIL-VERIFY (the fault proof, ci/faults/p2c/m3-pagination in the platform repository): anchor the
// cursor on the lookahead row again (`keys[pageSize]` in campaignItems) and
// TestPagination_CampaignReportWalksEveryItemExactlyOnce goes red naming the skipped item.

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"
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

// premise asserts the stamped rows are what the test needs: ONE displayed second, several distinct
// instants, and at least one tie. Without that the test cannot tell a second-precision cursor (or a
// tie-blind one) from a correct one.
func (e *testEnv) premise(t *testing.T, what, sql string, args ...any) {
	t.Helper()
	var seconds, instants, total int
	if err := e.admin.QueryRow(context.Background(), sql, args...).Scan(&seconds, &instants, &total); err != nil {
		t.Fatalf("premise query (%s): %v", what, err)
	}
	if seconds != 1 || instants <= 1 || instants >= total {
		t.Fatalf("premise broken (%s): %d row(s) display %d distinct second(s) and have %d distinct instants — want ONE second, several instants and at least one tie", what, total, seconds, instants)
	}
}

// ---- accounts.list ------------------------------------------------------------------------------

// seedOneSecondAccounts creates n accounts for a fresh user through the real create route, stamps them
// into one wall-clock second in tied pairs, asserts the premise, and returns the table's own ordering.
func (e *testEnv) seedOneSecondAccounts(t *testing.T, label string, n int) (string, []string) {
	t.Helper()
	userID := e.seedUser(t, label+"@example.com")
	for i := 0; i < n; i++ {
		e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "smtp.pg.example.com", fmt.Sprintf("%s-%d", label, i))).requireStatus(t, 201)
	}
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE send_byo.accounts a
		    SET created_at = TIMESTAMPTZ '2026-01-01 12:00:00+00' + (s.rn / 2) * INTERVAL '100 milliseconds' + INTERVAL '123 microseconds'
		   FROM (SELECT id, row_number() OVER (ORDER BY id) - 1 AS rn FROM send_byo.accounts WHERE user_id = $1) s
		  WHERE a.id = s.id`, userID); err != nil {
		t.Fatalf("stamp accounts: %v", err)
	}
	e.premise(t, "accounts",
		`SELECT count(DISTINCT date_trunc('second', created_at)), count(DISTINCT created_at), count(*)
		   FROM send_byo.accounts WHERE user_id = $1`, userID)
	want := e.idsInOrder(t, `SELECT id::text FROM send_byo.accounts WHERE user_id = $1 AND deleted_at IS NULL ORDER BY created_at, id`, userID)
	if len(want) != n {
		t.Fatalf("seeded %d accounts, the table holds %d", n, len(want))
	}
	return userID, want
}

// fetchPage reads one page of a list route (items[] + next_cursor) and returns its ids and cursor.
func (e *testEnv) fetchPage(t *testing.T, path string, limit int, cursor string) ([]string, string) {
	t.Helper()
	full := fmt.Sprintf("%s&limit=%d", path, limit)
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

// TestPagination_AccountsListWalksEveryRowExactlyOnce pages GET /v1/send-byo/accounts at several
// limits (1 puts a tie on every boundary, 3 puts a tied pair across one).
func TestPagination_AccountsListWalksEveryRowExactlyOnce(t *testing.T) {
	e := newTestEnv(t, "pgacct")
	userID, want := e.seedOneSecondAccounts(t, "pgacct", 7)
	// Another tenant's accounts must never enter this user's walk.
	other := e.seedUser(t, "pgacct-other@example.com")
	e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(other, "smtp.pg.example.com", "pgacct-other")).requireStatus(t, 201)

	for _, limit := range []int{1, 2, 3, 7, 50} {
		w := walkPages(limit, len(want)+3, func(cursor string) ([]string, string) {
			return e.fetchPage(t, "/v1/send-byo/accounts?user_id="+userID, limit, cursor)
		})
		if v := pageViolations(w, want); len(v) != 0 {
			t.Fatalf("GET /v1/send-byo/accounts limit=%d: %s\n  served: %v\n  table:  %v", limit, strings.Join(v, "; "), w.ids, want)
		}
	}
}

// ---- accounts.events ----------------------------------------------------------------------------

// seedOneSecondEvents creates one account and n account_events rows for it, stamped into one
// wall-clock second in tied pairs, and returns the account, its user and the table's own ordering of
// ALL its events (the seeded ones plus the real `created` row).
func (e *testEnv) seedOneSecondEvents(t *testing.T, label string, n int) (accountID, userID string, want []string) {
	t.Helper()
	userID = e.seedUser(t, label+"@example.com")
	accountID = e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "smtp.pg.example.com", label)).
		requireStatus(t, 201).str("id")
	if _, err := e.admin.Exec(context.Background(),
		`INSERT INTO send_byo.account_events (account_id, user_id, actor, action, reason, created_at)
		 SELECT $1::uuid, $2::uuid, 'user', 'verified', 'verify: success #' || g,
		        TIMESTAMPTZ '2026-01-01 12:00:00+00' + ((g - 1) / 2) * INTERVAL '100 milliseconds' + INTERVAL '123 microseconds'
		   FROM generate_series(1, $3::int) g`, accountID, userID, n); err != nil {
		t.Fatalf("seed events: %v", err)
	}
	e.premise(t, "events",
		`SELECT count(DISTINCT date_trunc('second', created_at)), count(DISTINCT created_at), count(*)
		   FROM send_byo.account_events WHERE account_id = $1 AND reason LIKE 'verify: success #%'`, accountID)
	want = e.idsInOrder(t, `SELECT id::text FROM send_byo.account_events WHERE account_id = $1 AND user_id = $2 ORDER BY created_at, id`, accountID, userID)
	if len(want) != n+1 {
		t.Fatalf("the account has %d events, want %d seeded + the real created row", len(want), n)
	}
	return accountID, userID, want
}

// TestPagination_AccountEventsWalkEveryRowExactlyOnce is the same walk over
// GET /v1/send-byo/accounts/{id}/events.
func TestPagination_AccountEventsWalkEveryRowExactlyOnce(t *testing.T) {
	e := newTestEnv(t, "pgevents")
	accountID, userID, want := e.seedOneSecondEvents(t, "pgevents", 7)

	for _, limit := range []int{1, 2, 3, 8, 50} {
		w := walkPages(limit, len(want)+3, func(cursor string) ([]string, string) {
			return e.fetchPage(t, "/v1/send-byo/accounts/"+accountID+"/events?user_id="+userID, limit, cursor)
		})
		if v := pageViolations(w, want); len(v) != 0 {
			t.Fatalf("GET /v1/send-byo/accounts/{id}/events limit=%d: %s\n  served: %v\n  table:  %v", limit, strings.Join(v, "; "), w.ids, want)
		}
	}
}

// oldSecondPrecisionPager reproduces the cursor construction accounts.list and accounts.events
// shipped with BEFORE the close-out: the next cursor is the DISPLAYED, second-precision created_at of
// the last row kept plus its id. It is test-local and independent of the handlers, so the control runs
// the OLD behavior over the SAME rows even though the handlers no longer have it.
func (e *testEnv) oldSecondPrecisionPager(t *testing.T, table, where string, whereArgs []any, limit int, cursor string) ([]string, string) {
	t.Helper()
	q := `SELECT id::text, to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"') FROM ` + table + ` WHERE ` + where
	args := append([]any{}, whereArgs...)
	if cursor != "" {
		parts := strings.SplitN(cursor, "|", 2)
		q += fmt.Sprintf(` AND (created_at, id) > ($%d::timestamptz, $%d::uuid)`, len(args)+1, len(args)+2)
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
		return ids[:limit], shown[limit-1] + "|" + ids[limit-1] // the OLD key: the DISPLAYED, second-precision value
	}
	return ids, ""
}

// TestPagination_ControlOldSecondPrecisionCursorIsDetected is the PAIRED CONTROL for the two
// second-precision listings: the old construction, through the same walker and checker over the same
// rows, must be a violation and — the case FW-M3-15 understated — a walk that never terminates.
func TestPagination_ControlOldSecondPrecisionCursorIsDetected(t *testing.T) {
	e := newTestEnv(t, "pgcontrol")
	userID, wantAccounts := e.seedOneSecondAccounts(t, "pgcontrol", 7)
	eventAccount, eventUser, wantEvents := e.seedOneSecondEvents(t, "pgcontrolev", 7)

	for _, c := range []struct {
		name, table, where string
		args               []any
		want               []string
	}{
		{"accounts", "send_byo.accounts", "user_id = $1 AND deleted_at IS NULL", []any{userID}, wantAccounts},
		{"events", "send_byo.account_events", "account_id = $1 AND user_id = $2", []any{eventAccount, eventUser}, wantEvents},
	} {
		w := walkPages(3, len(c.want)+3, func(cursor string) ([]string, string) {
			return e.oldSecondPrecisionPager(t, c.table, c.where, c.args, 3, cursor)
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

// ---- the campaign report's items ---------------------------------------------------------------

// seedOneCampaign creates n queued jobs of one campaign for a fresh user, stamps their created_at into
// tied pairs (so the id tiebreak sits on a page boundary at page_size 3), asserts there ARE ties, and
// returns the user, the campaign and the table's own ordering of the job_item ids.
func (e *testEnv) seedOneCampaign(t *testing.T, label string, n int) (userID, campaignID string, want []string) {
	t.Helper()
	userID = e.seedUser(t, label+"@example.com")
	campaignID = e.newCampaignID(t)
	for i := 0; i < n; i++ {
		e.campaignJobRow(t, campaignID, userID, "queued")
	}
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE send_byo.jobs j
		    SET created_at = TIMESTAMPTZ '2026-01-01 12:00:00+00' + (s.rn / 2) * INTERVAL '100 milliseconds' + INTERVAL '123 microseconds'
		   FROM (SELECT id, row_number() OVER (ORDER BY id) - 1 AS rn FROM send_byo.jobs WHERE campaign_id = $1) s
		  WHERE j.id = s.id`, campaignID); err != nil {
		t.Fatalf("stamp jobs: %v", err)
	}
	var instants, total int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(DISTINCT created_at), count(*) FROM send_byo.jobs WHERE campaign_id = $1`, campaignID).Scan(&instants, &total); err != nil {
		t.Fatalf("premise query: %v", err)
	}
	if instants <= 1 || instants >= total {
		t.Fatalf("premise broken: %d jobs have %d distinct created_at values — want several instants and at least one tie", total, instants)
	}
	want = e.idsInOrder(t, `SELECT job_item_id::text FROM send_byo.jobs WHERE campaign_id = $1 AND user_id = $2 ORDER BY created_at, id`, campaignID, userID)
	if len(want) != n {
		t.Fatalf("seeded %d jobs, the table holds %d", n, len(want))
	}
	return userID, campaignID, want
}

// fetchCampaignPage reads one page of GET /v1/report?campaign_id= items.
func (e *testEnv) fetchCampaignPage(t *testing.T, campaignID, userID string, pageSize int, cursor string) ([]string, string) {
	t.Helper()
	path := fmt.Sprintf("/v1/report?campaign_id=%s&user_id=%s&page_size=%d", campaignID, userID, pageSize)
	if cursor != "" {
		path += "&cursor=" + url.QueryEscape(cursor)
	}
	r := e.do(t, "GET", path, nil).requireStatus(t, 200)
	items, ok := r.body["items"].(map[string]any)
	if !ok {
		t.Fatalf("response has no items object: %s", r.raw)
	}
	results, ok := items["results"].([]any)
	if !ok {
		t.Fatalf("items has no results[]: %s", r.raw)
	}
	ids := make([]string, 0, len(results))
	for _, it := range results {
		m, ok := it.(map[string]any)
		if !ok {
			t.Fatalf("a result is not an object: %s", r.raw)
		}
		id, ok := m["job_item_id"].(string)
		if !ok {
			t.Fatalf("a result has no string job_item_id: %s", r.raw)
		}
		ids = append(ids, id)
	}
	next, _ := items["next_cursor"].(string)
	return ids, next
}

// TestPagination_CampaignReportWalksEveryItemExactlyOnce is the M2-G5 test: 7 items at page sizes
// 1, 2, 3, 4, 7 and 8 must each walk to exactly the table's 7 items. The cursor must anchor on the
// last row SERVED; one anchored on the lookahead row loses an item per page boundary.
func TestPagination_CampaignReportWalksEveryItemExactlyOnce(t *testing.T) {
	e := newTestEnv(t, "pgcampaign")
	userID, campaignID, want := e.seedOneCampaign(t, "pgcampaign", 7)

	// Another tenant's campaign must never enter this walk.
	e.seedOneCampaign(t, "pgcampaign-other", 3)

	for _, size := range []int{1, 2, 3, 4, 7, 8} {
		w := walkPages(size, len(want)+3, func(cursor string) ([]string, string) {
			return e.fetchCampaignPage(t, campaignID, userID, size, cursor)
		})
		if v := pageViolations(w, want); len(v) != 0 {
			t.Fatalf("GET /v1/report?campaign_id= page_size=%d: %s\n  served: %v\n  table:  %v", size, strings.Join(v, "; "), w.ids, want)
		}
	}
}

// oldLookaheadPager reproduces the campaign report's OLD cursor construction: the cursor is whatever
// the scan loop held LAST, which is the lookahead row, not the last row served. Test-local and
// independent of the handler.
func (e *testEnv) oldLookaheadPager(t *testing.T, campaignID, userID string, pageSize int, cursor string) ([]string, string) {
	t.Helper()
	q := `SELECT job_item_id::text, id::text, created_at FROM send_byo.jobs WHERE campaign_id = $1 AND user_id = $2`
	args := []any{campaignID, userID}
	if cursor != "" {
		parts := strings.SplitN(cursor, "|", 2)
		q += ` AND (created_at, id) > ($3::timestamptz, $4::uuid)`
		args = append(args, parts[0], parts[1])
	}
	q += fmt.Sprintf(` ORDER BY created_at ASC, id ASC LIMIT %d`, pageSize+1)
	rows, err := e.admin.Query(context.Background(), q, args...)
	if err != nil {
		t.Fatalf("old lookahead pager query: %v", err)
	}
	defer rows.Close()
	var ids []string
	var lastKey string // the OLD bug: overwritten on EVERY row scanned, so it ends holding the lookahead row
	for rows.Next() {
		var item, id string
		var at time.Time
		if err := rows.Scan(&item, &id, &at); err != nil {
			t.Fatalf("old lookahead pager scan: %v", err)
		}
		ids = append(ids, item)
		lastKey = at.UTC().Format(time.RFC3339Nano) + "|" + id
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("old lookahead pager rows: %v", err)
	}
	if len(ids) > pageSize {
		return ids[:pageSize], lastKey
	}
	return ids, ""
}

// TestPagination_ControlOldLookaheadCursorSkipsAnItemAndIsDetected is the PAIRED CONTROL for the
// campaign report: the OLD lookahead-anchored construction, through the same walker and checker over
// the same rows, must be reported as a GAP — one item skipped at the page boundary.
func TestPagination_ControlOldLookaheadCursorSkipsAnItemAndIsDetected(t *testing.T) {
	e := newTestEnv(t, "pgcampcontrol")
	userID, campaignID, want := e.seedOneCampaign(t, "pgcampcontrol", 7)

	w := walkPages(3, len(want)+3, func(cursor string) ([]string, string) {
		return e.oldLookaheadPager(t, campaignID, userID, 3, cursor)
	})
	v := pageViolations(w, want)
	if len(v) == 0 {
		t.Fatalf("CONTROL FAILED: the OLD lookahead-anchored cursor walked exactly the table's items — the checker cannot detect M2-G5, so the production walk proves nothing; served %v", w.ids)
	}
	if !strings.Contains(strings.Join(v, "; "), "missing (a gap)") {
		t.Fatalf("CONTROL: the old construction was reported (%v) but not as a GAP; M2-G5 is a skipped item", v)
	}
	if len(w.ids) != len(want)-1 {
		t.Fatalf("CONTROL: the old construction served %d of %d items, want exactly one skipped (the lookahead row at the first page boundary): %v", len(w.ids), len(want), w.ids)
	}
}
