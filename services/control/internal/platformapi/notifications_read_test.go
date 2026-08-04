package platformapi

// notifications_read_test.go — audit D5 / fix-plan item 9: GET /v1/send-byo/notifications.
// Real Postgres, real HTTP against the actual production Server (CLAUDE.md Rule 3).
//
// THE DEFECT THIS ROUTE FIXES, and therefore what these tests have to prove. send_byo.notifications
// had two write sites and no reader outside dispatch's own delivery loop, whose "delivery" is a pure
// in-DB state flip (internal/dispatch imports no net/http). Rows were stamped 'delivered' with
// notified_at set and no recipient had ever seen them. So the property under test is not "a list
// endpoint returns a list" — it is that the rows the PRODUCTION WRITERS actually produce, in the
// states the production DELIVERY LOOP actually leaves them in, come back to the tenant that owns
// them and to nobody else.
//
// Every seed below therefore writes the exact column values the two dispatch writers write
// (condition/subject_type/severity/dedup_key/payload verbatim from account_effect.go:172 and
// campaign_expiry.go:155), and the states come from notify.go's real transitions rather than from
// whatever is convenient.

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// seedNotificationAt inserts one notifications row with FULL control over state/notified_at/
// cleared_at/first_seen_at, so a test can reproduce any state notify.go can leave behind.
//
// firstSeen is a SQL expression, not a bound value, so a test can say "NOW()" or an explicit
// microsecond-precision literal. The keyset tests need the latter: two rows inside the SAME second
// is the case a truncated cursor gets wrong, and NOW() cannot express it deterministically.
func (e *testEnv) seedNotificationAt(t *testing.T, userID, condition, subjectType, subjectID,
	severity, dedupKey, state, notifiedAt, clearedAt, payload, firstSeen string) string {
	t.Helper()
	var id string
	q := `INSERT INTO send_byo.notifications
	        (user_id, condition, subject_type, subject_id, severity, dedup_key, state,
	         notified_at, cleared_at, payload, first_seen_at, last_seen_at)
	      VALUES ($1,$2,$3,$4::uuid,$5::send_byo.notification_severity,$6,
	              $7::send_byo.notification_state,` + notifiedAt + `,` + clearedAt + `,$8::jsonb,` +
		firstSeen + `,` + firstSeen + `) RETURNING id`
	if err := e.admin.QueryRow(context.Background(), q, userID, condition, subjectType, subjectID,
		severity, dedupKey, state, payload).Scan(&id); err != nil {
		t.Fatalf("seed notification (%s/%s): %v", condition, state, err)
	}
	return id
}

// seedQuarantineNotificationDelivered writes the row the account_quarantined path ACTUALLY leaves
// behind after a NotificationTick: state 'delivered', notified_at set, cleared_at NULL, and the
// payload account_effect.go:172 constructs — {account_id, reason}.
//
// This is the row that was invisible before this route existed, and it is invisible in the way that
// matters: 'delivered' is terminal for the delivery loop, so no retry or backfill will ever revisit
// it. A read surface that only showed 'pending' rows would return nothing here.
func (e *testEnv) seedQuarantineNotificationDelivered(t *testing.T, userID, acctID, reason string) string {
	t.Helper()
	return e.seedNotificationAt(t, userID, "account_quarantined", "account", acctID, "critical",
		"account_quarantined:"+acctID, "delivered", "NOW()", "NULL",
		`{"account_id":"`+acctID+`","reason":"`+reason+`"}`, "NOW()")
}

// notificationItems pulls items[] out of a response as a typed slice, failing on a wrong shape
// rather than silently yielding an empty list (which would make every count assertion vacuous).
func notificationItems(t *testing.T, r resp) []map[string]any {
	t.Helper()
	raw, ok := r.body["items"]
	if !ok {
		t.Fatalf("response has no items[] key — the declared shape is {items, next_cursor}: %s", r.raw)
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("items is %T, want an array: %s", raw, r.raw)
	}
	out := make([]map[string]any, 0, len(list))
	for i, it := range list {
		m, ok := it.(map[string]any)
		if !ok {
			t.Fatalf("items[%d] is %T, want an object: %s", i, it, r.raw)
		}
		out = append(out, m)
	}
	return out
}

func notificationIDs(items []map[string]any) []string {
	out := make([]string, 0, len(items))
	for _, m := range items {
		s, _ := m["id"].(string)
		out = append(out, s)
	}
	return out
}

// --- 1. The whole point of the item: what dispatch wrote, the owner can read ---

// TestNotificationsRead_DeliveredQuarantineRowIsVisible — the dispositive test for D5. A row in the
// EXACT state the production quarantine path leaves behind (state 'delivered', notified_at set,
// nothing ever egressed) is returned to its owner with every field a UI needs to render it.
//
// FAIL-VERIFY: this test fails outright with the route absent (404), and it fails if the handler
// filters on state (the row is 'delivered', which is what every pre-existing row already is).
func TestNotificationsRead_DeliveredQuarantineRowIsVisible(t *testing.T) {
	e := newTestEnv(t, "notifread")
	userID := e.seedUser(t, "notifread@example.com")
	acctID := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "smtp.notifread.com", "notifread-user")).
		requireStatus(t, 201).str("id")

	e.seedQuarantineNotificationDelivered(t, userID, acctID, "auth_failure")

	got := e.do(t, "GET", "/v1/send-byo/notifications?user_id="+userID, nil).requireStatus(t, 200)
	items := notificationItems(t, got)
	if len(items) != 1 {
		t.Fatalf("got %d notifications, want 1 — the row dispatch wrote must be readable by its owner "+
			"(this is defect D5: recorded, delivered, never actually delivered anywhere): %s", len(items), got.raw)
	}
	n := items[0]

	// Every field, checked by name. A UI renders "which condition, about what, how bad, is it
	// resolved" — if any one of these is missing the row is present but useless.
	if n["condition"] != "account_quarantined" {
		t.Fatalf("condition=%v, want account_quarantined (the string account_effect.go writes): %s", n["condition"], got.raw)
	}
	if n["subject_type"] != "account" || n["subject_id"] != acctID {
		t.Fatalf("subject=%v/%v, want account/%s — without the subject a client cannot tell WHICH account is quarantined: %s",
			n["subject_type"], n["subject_id"], acctID, got.raw)
	}
	if n["severity"] != "critical" {
		t.Fatalf("severity=%v, want critical: %s", n["severity"], got.raw)
	}
	if n["state"] != "delivered" {
		t.Fatalf("state=%v, want delivered — the state the row was seeded in; if the handler rewrote or "+
			"filtered it this assertion is the one that catches it: %s", n["state"], got.raw)
	}
	if n["user_id"] != userID {
		t.Fatalf("user_id=%v, want %s: %s", n["user_id"], userID, got.raw)
	}
	// notified_at PRESENT and cleared_at NULL: this is precisely the D5 state — marked as notified,
	// never actually seen, condition still live. cleared_at must be an explicit null rather than an
	// omitted key, so a client can distinguish "not resolved" from "this field does not exist".
	if s, ok := n["notified_at"].(string); !ok || s == "" {
		t.Fatalf("notified_at=%v, want a timestamp string: %s", n["notified_at"], got.raw)
	}
	if v, present := n["cleared_at"]; !present || v != nil {
		t.Fatalf("cleared_at=%v (present=%v), want an explicit null — a UI reads THIS field, not state, to "+
			"decide whether the condition resolved: %s", v, present, got.raw)
	}
	for _, f := range []string{"first_seen_at", "last_seen_at"} {
		if s, ok := n[f].(string); !ok || !strings.HasSuffix(s, "Z") {
			t.Fatalf("%s=%v, want a UTC ...Z timestamp (the module's standard form): %s", f, n[f], got.raw)
		}
	}
	// payload verbatim, including the quarantine reason — which this surface ALREADY exposes as
	// accountResource.status.quarantine_reason, so it discloses nothing new. Checked because it is
	// what makes the notification actionable: "quarantined" alone does not tell a user to go fix a
	// password.
	pl, ok := n["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload=%T, want an object: %s", n["payload"], got.raw)
	}
	if pl["account_id"] != acctID || pl["reason"] != "auth_failure" {
		t.Fatalf("payload=%v, want {account_id:%s, reason:auth_failure} — the keys account_effect.go writes: %s",
			pl, acctID, got.raw)
	}
	// next_cursor is an explicit null on a single short page, not an omitted key.
	if v, present := got.body["next_cursor"]; !present || v != nil {
		t.Fatalf("next_cursor=%v (present=%v), want an explicit null on a single-page result: %s", v, present, got.raw)
	}

	// TWO-VOCABULARY (Rule 5): nothing internal crossed the boundary with the payload.
	if containsAny(got.raw, "credential_ciphertext", "smtp_secret", "dedup_key", "SQLSTATE", "send_byo.") {
		t.Fatalf("the notifications response leaked an internal identifier: %s", got.raw)
	}
}

// TestNotificationsRead_CampaignExpiryRowIsVisible — the OTHER production writer. campaign_expiry.go
// INSERTs directly rather than going through EnqueueNotification, with a different condition,
// subject_type, severity and payload shape, and it never sets notified_at — so it exercises the
// nullable-timestamp and warning-severity paths the quarantine row does not.
func TestNotificationsRead_CampaignExpiryRowIsVisible(t *testing.T) {
	e := newTestEnv(t, "notifreadcamp")
	userID := e.seedUser(t, "notifreadcamp@example.com")
	campaignID := e.seedUser(t, "notifreadcamp-campaign@example.com") // any real UUID; subject_id has no FK

	e.seedNotificationAt(t, userID, "campaign_expired", "campaign", campaignID, "warning",
		"campaign_expired:"+campaignID, "pending", "NULL", "NULL",
		`{"campaign_id":"`+campaignID+`"}`, "NOW()")

	items := notificationItems(t, e.do(t, "GET", "/v1/send-byo/notifications?user_id="+userID, nil).requireStatus(t, 200))
	if len(items) != 1 {
		t.Fatalf("got %d notifications, want 1", len(items))
	}
	n := items[0]
	if n["condition"] != "campaign_expired" || n["subject_type"] != "campaign" || n["severity"] != "warning" {
		t.Fatalf("got %v/%v/%v, want campaign_expired/campaign/warning — the values campaign_expiry.go writes: %v",
			n["condition"], n["subject_type"], n["severity"], n)
	}
	// notified_at is NULL on a 'pending' row and must serialize as an explicit null, not "" or a
	// zero time — a client renders "not yet notified" off this.
	if v, present := n["notified_at"]; !present || v != nil {
		t.Fatalf("notified_at=%v (present=%v), want an explicit null on a pending row: %v", v, present, n)
	}
	pl, _ := n["payload"].(map[string]any)
	if pl["campaign_id"] != campaignID {
		t.Fatalf("payload=%v, want {campaign_id:%s}", pl, campaignID)
	}
}

// TestNotificationsRead_EveryStateIsReturned — the load-bearing design decision, pinned. The route
// applies NO state filter, and that is not laziness: every row written before this route existed is
// already 'delivered' or 'suppressed' (notify.go's terminal states, with no egress behind either),
// and nothing revisits them, so a default filter narrower than "everything" would make this route
// return an empty list on every real installation — i.e. would ship the D5 defect a second time
// wearing a route.
//
// FAIL-VERIFY: add `AND state = 'pending'` (or `AND cleared_at IS NULL`) to the handler's conds and
// this fails, naming the missing state.
func TestNotificationsRead_EveryStateIsReturned(t *testing.T) {
	e := newTestEnv(t, "notifreadstates")
	userID := e.seedUser(t, "notifreadstates@example.com")
	subject := e.seedUser(t, "notifreadstates-subject@example.com")

	// One row per state the enum declares as of send_byo_0047. Distinct dedup_keys because
	// notifications_live_dedup_idx is UNIQUE (user_id, dedup_key) WHERE cleared_at IS NULL.
	//
	// 'cleared' is included even though dispatch NEVER writes it (ClearNotification stamps cleared_at
	// and leaves state alone — the declared-but-unused value describe_routes.go's schema comment
	// flags). It is here so that the day something does start writing it, this route already returns
	// it rather than hiding those rows.
	states := []struct{ state, notifiedAt, clearedAt, key string }{
		{"pending", "NULL", "NULL", "c_pending:" + subject},
		{"delivered", "NOW()", "NULL", "c_delivered:" + subject},
		{"suppressed", "NOW()", "NULL", "c_suppressed:" + subject},
		// A CLEARED row: the condition resolved. Still returned — the notification history is an audit
		// trail (the #134 fix's own words: "cleared, not deleted"), and a UI showing only live rows
		// cannot render "this was resolved".
		{"delivered", "NOW()", "NOW()", "c_resolved:" + subject},
		{"cleared", "NOW()", "NOW()", "c_clearedstate:" + subject},
	}
	for _, s := range states {
		e.seedNotificationAt(t, userID, "account_quarantined", "account", subject, "critical",
			s.key, s.state, s.notifiedAt, s.clearedAt, `{"account_id":"`+subject+`"}`, "NOW()")
	}

	items := notificationItems(t, e.do(t, "GET", "/v1/send-byo/notifications?user_id="+userID, nil).requireStatus(t, 200))
	if len(items) != len(states) {
		t.Fatalf("got %d notifications, want %d — the route must apply NO state filter: every row for the "+
			"tenant, in every state, including cleared ones", len(items), len(states))
	}
	seen := map[string]int{}
	cleared := 0
	for _, n := range items {
		seen[n["state"].(string)]++
		if n["cleared_at"] != nil {
			cleared++
		}
	}
	for _, want := range []string{"pending", "delivered", "suppressed", "cleared"} {
		if seen[want] == 0 {
			t.Fatalf("no row with state %q came back (got %v) — a state filter would silently hide "+
				"precisely the rows that already exist in every deployment", want, seen)
		}
	}
	if cleared != 2 {
		t.Fatalf("%d rows had cleared_at set, want 2 — a `cleared_at IS NULL` filter would hide resolved "+
			"history, which is the audit trail", cleared)
	}
}

// --- 2. Tenancy: the control that makes this route safe to add at all ---

// TestNotificationsRead_CrossTenantSeesNothing — the negative control, and the one that would FAIL if
// the WHERE clause were dropped or turned into an optional filter. Tenant B, authenticated with the
// same real service token, asks for tenant A's notifications using B's OWN selector and gets an empty
// list that is BYTE-IDENTICAL to the empty list a tenant with no notifications gets. No 403, no
// count, no existence disclosure of any kind.
//
// FAIL-VERIFY: delete `n.user_id = $1` from conds and this fails on the first assertion (B sees A's
// rows). Weaken it to a non-scoping filter and the byte-identity assertion still catches it.
func TestNotificationsRead_CrossTenantSeesNothing(t *testing.T) {
	e := newTestEnv(t, "notifreadtenancy")
	userA := e.seedUser(t, "notifread-a@example.com")
	userB := e.seedUser(t, "notifread-b@example.com")
	userC := e.seedUser(t, "notifread-c@example.com") // never has any notification at all
	acctA := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userA, "smtp.notifreada.com", "notifread-a")).
		requireStatus(t, 201).str("id")

	e.seedQuarantineNotificationDelivered(t, userA, acctA, "auth_failure")

	// Anti-vacuity FIRST: A really can see it, so B's empty result means "scoped out", not "the seed
	// silently failed" or "the route is broken for everyone".
	if got := notificationItems(t, e.do(t, "GET", "/v1/send-byo/notifications?user_id="+userA, nil).requireStatus(t, 200)); len(got) != 1 {
		t.Fatalf("PRECONDITION FAILED: owner A sees %d rows, want 1 — without a visible row this test proves nothing", len(got))
	}

	// B asks with B's own valid selector. B is genuinely authenticated (same shared service token).
	bResp := e.do(t, "GET", "/v1/send-byo/notifications?user_id="+userB, nil).requireStatus(t, 200)
	if items := notificationItems(t, bResp); len(items) != 0 {
		t.Fatalf("tenant B sees %d of tenant A's notifications: %s", len(items), bResp.raw)
	}
	// THE NO-DISCLOSURE ASSERTION: B's response is indistinguishable from C's, and C has no
	// notifications anywhere in the table. A count, a differing next_cursor, or any hint that
	// something exists-but-is-hidden would break this.
	cResp := e.do(t, "GET", "/v1/send-byo/notifications?user_id="+userC, nil).requireStatus(t, 200)
	if bResp.raw != cResp.raw {
		t.Fatalf("a tenant whose notifications are SCOPED OUT gets a different response from a tenant with "+
			"NONE — that difference is an existence oracle.\nB (A's rows exist): %s\nC (nothing exists):  %s",
			bResp.raw, cResp.raw)
	}
	// And neither response mentions A's ids anywhere, including outside items[].
	if containsAny(bResp.raw, acctA, userA) {
		t.Fatalf("tenant B's response mentions tenant A's account/user id: %s", bResp.raw)
	}
	// Even asking with A's own selector while… well, there is only one shared service token on this
	// surface (OD-25 option b: the module enforces tenancy off the selector), so the meaningful probe
	// is the one above. What must ALSO hold is that a nonexistent-but-well-formed tenant behaves the
	// same as a scoped-out one — the third leg of the same property.
	ghost := e.do(t, "GET", "/v1/send-byo/notifications?user_id=00000000-0000-4000-8000-000000000000", nil).
		requireStatus(t, 200)
	if ghost.raw != cResp.raw {
		t.Fatalf("an unknown tenant id gets a different response from a known-but-empty one — another "+
			"existence oracle:\nghost: %s\nempty: %s", ghost.raw, cResp.raw)
	}
}

// TestNotificationsRead_TenantSelectorRequired — the same user_id vocabulary as every sibling read.
// One client renders all of them identically, which is the alignment property item 9 sits inside.
func TestNotificationsRead_TenantSelectorRequired(t *testing.T) {
	e := newTestEnv(t, "notifreadsel")

	missing := e.do(t, "GET", "/v1/send-byo/notifications", nil).requireStatus(t, 400)
	if missing.errorCode() != codeValidationFailed {
		t.Fatalf("no user_id: code=%s, want %s", missing.errorCode(), codeValidationFailed)
	}
	if got := missing.errorDetailFields(); len(got) != 1 || got[0] != "user_id:"+issueRequired {
		t.Fatalf("no user_id: details=%v, want [user_id:%s] — the same slug accounts.list/accounts.events use: %s",
			got, issueRequired, missing.raw)
	}
	// url.QueryEscape is REQUIRED, not tidiness. The SQL-injection probe contains spaces, and
	// http.NewRequest puts a raw query string into the request line verbatim: net/http's own server
	// then rejects the malformed request line with a bare text/plain "400 Bad Request" and an EMPTY
	// body, before any handler runs. That still satisfies requireStatus(t, 400), so an unencoded probe
	// PASSES for entirely the wrong reason — the handler never saw the input and the envelope was
	// never produced. The assertion on details[] below is what exposed it (details=[] against
	// [user_id:malformed]); the encode is what makes the probe actually reach the route.
	for _, bad := range []string{"not-a-uuid", "12345", "'; DROP TABLE send_byo.notifications; --"} {
		r := e.do(t, "GET", "/v1/send-byo/notifications?user_id="+url.QueryEscape(bad), nil).requireStatus(t, 400)
		// The rejection must be OUR envelope, not the server's pre-handler 400. Without this, the
		// same silent pass is one un-encoded character away from returning.
		if r.errorCode() != codeValidationFailed {
			t.Fatalf("user_id=%q: code=%q, want %q — a bare non-envelope 400 means the request never "+
				"reached the handler (check the query encoding), not that the route validated anything: %q",
				bad, r.errorCode(), codeValidationFailed, r.raw)
		}
		if got := r.errorDetailFields(); len(got) != 1 || got[0] != "user_id:"+issueMalformed {
			t.Fatalf("user_id=%q: details=%v, want [user_id:%s]: %s", bad, got, issueMalformed, r.raw)
		}
		if msg := r.errorMessageOnly(); msg != fixedMessages[codeValidationFailed] {
			t.Fatalf("user_id=%q: message=%q, want the fixed %q (two-vocabulary)", bad, msg, fixedMessages[codeValidationFailed])
		}
		if containsAny(r.raw, "DROP TABLE") {
			t.Fatalf("the rejection echoed caller-supplied input: %s", r.raw)
		}
	}
	// The table still exists — the injection probe above was rejected as a malformed selector, not
	// executed. Cheap, and it makes the probe's NAME true rather than aspirational.
	var alive bool
	if err := e.pool.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM information_schema.tables
		   WHERE table_schema='send_byo' AND table_name='notifications')`).Scan(&alive); err != nil {
		t.Fatalf("post-injection table check: %v", err)
	}
	if !alive {
		t.Fatal("send_byo.notifications no longer exists after the injection probe")
	}
}

// --- 3. Pagination, including the sub-second cursor bug the siblings have and this route does not ---

// TestNotificationsRead_KeysetPagesWithoutDuplicatingASubSecondBoundaryRow — the cursor test that
// actually bites. The displayed timestamp is second-precision (to_char ...HH24:MI:SS"Z"), so a cursor
// built from the DISPLAYED value is strictly EARLIER than the row it points at whenever that row's
// first_seen_at has a fractional part: the keyset predicate
// (first_seen_at, id) > (cursor_ts, cursor_id) then re-matches the boundary row itself and page 2
// begins with a duplicate of page 1's last row. Three rows 400ms apart inside ONE second is an
// ordinary write pattern here (a paced bulk-add quarantining several accounts, one expiry sweep
// covering several campaigns).
//
// FAIL-VERIFY: change notificationSelectCols' last projection from .US precision to plain SS (i.e.
// feed encodeAccountCursor the displayed value, exactly as the sibling routes do) and this test fails
// with a duplicated id on page 2.
func TestNotificationsRead_KeysetPagesWithoutDuplicatingASubSecondBoundaryRow(t *testing.T) {
	e := newTestEnv(t, "notifreadcursor")
	userID := e.seedUser(t, "notifreadcursor@example.com")
	subject := e.seedUser(t, "notifreadcursor-subject@example.com")

	// All three inside the same wall-clock second, in ascending order.
	want := make([]string, 0, 3)
	for i, frac := range []string{"0.100", "0.500", "0.900"} {
		want = append(want, e.seedNotificationAt(t, userID, "account_quarantined", "account", subject,
			"critical", "cursor"+strconv.Itoa(i)+":"+subject, "delivered", "NOW()", "NULL",
			`{"account_id":"`+subject+`"}`,
			"'2026-01-01T12:00:0"+frac+"Z'::timestamptz"))
	}

	// Anti-vacuity on the PREMISE: the displayed timestamps really are truncated to the same second,
	// so a display-derived cursor really would be ambiguous. If a future change widened the displayed
	// precision this test would still pass while no longer testing anything — so assert the premise.
	all := e.do(t, "GET", "/v1/send-byo/notifications?user_id="+userID, nil).requireStatus(t, 200)
	allItems := notificationItems(t, all)
	if len(allItems) != 3 {
		t.Fatalf("got %d rows, want 3", len(allItems))
	}
	shown := map[string]bool{}
	for _, n := range allItems {
		shown[n["first_seen_at"].(string)] = true
	}
	if len(shown) != 1 {
		t.Fatalf("the three rows display %d distinct first_seen_at values (%v), want 1 — this test is only "+
			"meaningful while the DISPLAYED timestamp is second-precision and therefore ambiguous", len(shown), shown)
	}

	// Now page through one row at a time and assert we walk exactly the seeded order with no repeats.
	seenIDs := []string{}
	cursor := ""
	for page := 0; page < 5; page++ {
		url := "/v1/send-byo/notifications?user_id=" + userID + "&limit=1"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		r := e.do(t, "GET", url, nil).requireStatus(t, 200)
		items := notificationItems(t, r)
		if len(items) == 0 {
			break
		}
		if len(items) != 1 {
			t.Fatalf("limit=1 returned %d rows: %s", len(items), r.raw)
		}
		id := items[0]["id"].(string)
		for _, prior := range seenIDs {
			if prior == id {
				t.Fatalf("page %d repeated row %s — the cursor points BEFORE the row it was built from, so the "+
					"keyset re-matches the boundary row. This is what a second-precision cursor key does to "+
					"rows that share a second: %s", page, id, r.raw)
			}
		}
		seenIDs = append(seenIDs, id)
		next, ok := r.body["next_cursor"].(string)
		if !ok {
			break
		}
		cursor = next
	}
	if len(seenIDs) != 3 {
		t.Fatalf("paged %d rows one at a time, want 3 (%v vs seeded %v) — a page that ends early loses rows a "+
			"user needs to see", len(seenIDs), seenIDs, want)
	}
	for i := range want {
		if seenIDs[i] != want[i] {
			t.Fatalf("page order = %v, want the seeded ascending order %v — an ascending keyset is what makes "+
				"\"everything since I last looked\" work with a saved cursor", seenIDs, want)
		}
	}
}

// TestNotificationsRead_LimitAndMalformedCursor — the shared pagination vocabulary: limit clamps at
// maxLimit, junk limits fall back to the default rather than erroring, and a malformed cursor is a
// coded 400 naming the field.
func TestNotificationsRead_LimitAndMalformedCursor(t *testing.T) {
	e := newTestEnv(t, "notifreadlimit")
	userID := e.seedUser(t, "notifreadlimit@example.com")
	subject := e.seedUser(t, "notifreadlimit-subject@example.com")
	for i := 0; i < 3; i++ {
		e.seedNotificationAt(t, userID, "account_quarantined", "account", subject, "critical",
			"lim"+strconv.Itoa(i)+":"+subject, "delivered", "NOW()", "NULL",
			`{"account_id":"`+subject+`"}`, "NOW()")
	}

	// limit=2 of 3 → a page of 2 AND a cursor (the lookahead saw the third).
	two := e.do(t, "GET", "/v1/send-byo/notifications?user_id="+userID+"&limit=2", nil).requireStatus(t, 200)
	if items := notificationItems(t, two); len(items) != 2 {
		t.Fatalf("limit=2 returned %d rows, want 2: %s", len(items), two.raw)
	}
	if _, ok := two.body["next_cursor"].(string); !ok {
		t.Fatalf("limit=2 over 3 rows returned no next_cursor — the remaining row is unreachable: %s", two.raw)
	}
	// The lookahead row must not LEAK into the page: exactly limit rows, never limit+1.
	if items := notificationItems(t, e.do(t, "GET", "/v1/send-byo/notifications?user_id="+userID+"&limit=1", nil).
		requireStatus(t, 200)); len(items) != 1 {
		t.Fatalf("limit=1 returned %d rows — the limit+1 lookahead row must be trimmed before writing", len(items))
	}
	// A junk / zero / over-max limit is tolerated, not an error (parseLimit's documented behavior,
	// shared with accounts.list). Asserted so this route cannot drift from its siblings.
	for _, q := range []string{"limit=abc", "limit=0", "limit=-5", "limit=99999"} {
		r := e.do(t, "GET", "/v1/send-byo/notifications?user_id="+userID+"&"+q, nil).requireStatus(t, 200)
		if items := notificationItems(t, r); len(items) != 3 {
			t.Fatalf("%s returned %d rows, want all 3 (junk/zero/over-max limits fall back or clamp, they do "+
				"not error — same as accounts.list): %s", q, len(items), r.raw)
		}
	}
	// A malformed cursor IS an error, with the field named — the same 400 accounts.list gives.
	bad := e.do(t, "GET", "/v1/send-byo/notifications?user_id="+userID+"&cursor=!!!not-base64!!!", nil).
		requireStatus(t, 400)
	if got := bad.errorDetailFields(); len(got) != 1 || got[0] != "cursor:"+issueMalformed {
		t.Fatalf("malformed cursor: details=%v, want [cursor:%s]: %s", got, issueMalformed, bad.raw)
	}
}

// --- 4. The structural control on payload exposure ---

// notificationInsertPattern matches a direct INSERT into the notifications table in production code.
var notificationInsertPattern = regexp.MustCompile(`INSERT INTO send_byo\.notifications`)

// notificationEnqueuePattern matches any reference to the EnqueueNotification helper — its
// definition AND its callers. Deliberately not a caller-only pattern: Go has no lookbehind, and every
// "match the call but not `func X(`" formulation is a guess about call syntax (`if err := X(`,
// `return X(`, `err = X(`) that a legitimate third caller could dodge. Matching every reference and
// listing the definition file as one of the expected sites cannot be dodged.
var notificationEnqueuePattern = regexp.MustCompile(`EnqueueNotification\(`)

// TestNotificationsRead_PayloadWriteSitesArePinned — the control that keeps `payload` safe to return
// VERBATIM.
//
// WHY A SOURCE-LEVEL TEST IS THE RIGHT INSTRUMENT. The handler returns payload as an opaque JSONB
// blob, deliberately: an allowlist projection would silently drop the payload of any condition added
// later, leaving a UI able to see a notification it cannot render. That choice is safe only while
// every writer is known and every writer's payload is ids-and-enums. A behavioral test cannot check
// that — it can only check the payloads it thought to write. So pin the WRITE SITES instead: adding a
// third one fails here, and whoever adds it has to look at what their payload contains and at this
// route before the test goes green again. Same instrument, same reason, as
// account_autoclear_test.go's credential_ciphertext write-site grep.
//
// FAIL-VERIFY: add a third INSERT INTO send_byo.notifications anywhere in non-test code and this
// fails naming the file.
func TestNotificationsRead_PayloadWriteSitesArePinned(t *testing.T) {
	inserts := grepProductionGo(t, notificationInsertPattern)
	enqueues := grepProductionGo(t, notificationEnqueuePattern)

	// The two known INSERT sites: EnqueueNotification's own upsert, and campaign_expiry's direct
	// INSERT (which bypasses EnqueueNotification because it needs DO NOTHING rather than the
	// re-notify CASE ladder).
	wantInserts := []string{
		"internal/dispatch/notify.go",
		"internal/dispatch/campaign_expiry.go",
	}
	// The EnqueueNotification sites: its definition, and its ONE caller (the auto-quarantine
	// transition). notify.go appears in both lists — it both defines the helper and contains the
	// upsert — which is correct, not a duplicate: the two patterns answer two different questions.
	wantEnqueues := []string{
		"internal/dispatch/notify.go",
		"internal/dispatch/account_effect.go",
	}

	assertSiteSet := func(label string, got []string, want []string) {
		t.Helper()
		gotFiles := map[string]bool{}
		for _, hit := range got {
			gotFiles[filepath.ToSlash(strings.SplitN(hit, ":", 2)[0])] = true
		}
		if len(gotFiles) != len(want) {
			t.Fatalf("%s: found %d file(s) %v, want exactly %d %v.\n"+
				"A NEW notifications writer must be reviewed against platformapi/notifications_read.go, which "+
				"returns `payload` VERBATIM to the owning user. If the new payload carries only ids and enum "+
				"values (as both existing writers do), add it to this list. If it carries provider text, an "+
				"error string, a recipient address, or anything else from the two-vocabulary internal side, the "+
				"read route must be narrowed BEFORE this test is updated.", label, len(gotFiles), keysOf(gotFiles), len(want), want)
		}
		for _, w := range want {
			if !gotFiles[w] {
				t.Fatalf("%s: expected writer %s not found (found %v) — if it was removed or renamed, update this "+
					"list; if the grep pattern stopped matching, fix the pattern rather than the expectation",
					label, w, keysOf(gotFiles))
			}
		}
	}
	assertSiteSet("INSERT INTO send_byo.notifications", inserts, wantInserts)
	assertSiteSet("EnqueueNotification callers", enqueues, wantEnqueues)

	// Anti-vacuity: a pattern that matches nothing would make both assertions above pass only if
	// `want` were also empty — but a typo'd pattern plus a stale non-empty want would fail loudly,
	// which is the behavior we want. Assert non-emptiness explicitly anyway, so a future refactor that
	// legitimately empties one list cannot leave a silently-dead check behind.
	if len(inserts) == 0 || len(enqueues) == 0 {
		t.Fatalf("one of the grep patterns matched NOTHING (inserts=%d, enqueues=%d) — this control is vacuous "+
			"until the pattern matches real code", len(inserts), len(enqueues))
	}

	// The Go-side mirror of tests/harness/checks/user_output_scrub.sh check 3, so `go test` catches
	// this without waiting for CI. Two different needle sets, because the two file groups have two
	// different obligations:
	//
	//   The READ ROUTE must not name ANY of the internal-vocabulary columns. It is the boundary.
	//   The WRITERS must not name the provider-response column specifically (check 3's own needle) —
	//   deliberately NOT last_error_message, because campaign_expiry.go legitimately WRITES
	//   send_byo.jobs.last_error_message in the same function (the expiry reason on the failed job, an
	//   internal column that no user surface reads). Widening the needle set for the writers would
	//   fail on that correct line, and the fix would have been to exempt the file — which is how a
	//   check quietly stops checking. Narrow needle, no exemptions.
	readRouteNeedles := []string{"last_smtp_response", "last_error_message", "credential_ciphertext", "smtp_secret"}
	writerNeedles := []string{"last_smtp_response"}
	mustNotMention := func(f string, needles []string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(servicesControlRoot(t), filepath.FromSlash(f)))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, needle := range needles {
			if strings.Contains(string(data), needle) {
				t.Fatalf("%s mentions %q — `payload` is returned VERBATIM to the owning user by "+
					"platformapi/notifications_read.go, so neither that route nor any notifications writer may "+
					"touch the provider-text/credential columns", f, needle)
			}
		}
	}
	mustNotMention("internal/platformapi/notifications_read.go", readRouteNeedles)
	for _, f := range append(append([]string{}, wantInserts...), wantEnqueues...) {
		mustNotMention(f, writerNeedles)
	}
}

// grepProductionGo returns "relpath:line" for every non-test .go file under services/control whose
// content matches pat. Same walk as account_autoclear_test.go's, generalized over the pattern.
func grepProductionGo(t *testing.T, pat *regexp.Regexp) []string {
	t.Helper()
	root := servicesControlRoot(t)
	var hits []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for i, line := range strings.Split(string(data), "\n") {
			if pat.MatchString(line) {
				rel, _ := filepath.Rel(root, path)
				hits = append(hits, filepath.ToSlash(rel)+":"+strconv.Itoa(i+1))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return hits
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// --- 5. The declaration link ---

// TestNotificationsRead_IsDeclaredInDescribe — the route was added to extensionRoutes(), so describe
// declares it BY CONSTRUCTION and server.go mounts it from the same slice. That is the mechanism, but
// a mechanism nobody asserts is a comment: this drives the route using ONLY the method and path
// describe reported, and checks the response really matches the schema fragment describe pointed at.
//
// (describe_routes_test.go's own len(routes) != len(registered) control covers the other direction —
// a registered route that describe fails to render.)
func TestNotificationsRead_IsDeclaredInDescribe(t *testing.T) {
	e := newTestEnv(t, "notifreaddescribe")
	userID := e.seedUser(t, "notifreaddescribe@example.com")
	subject := e.seedUser(t, "notifreaddescribe-subject@example.com")
	e.seedNotificationAt(t, userID, "account_quarantined", "account", subject, "critical",
		"describe:"+subject, "delivered", "NOW()", "NULL", `{"account_id":"`+subject+`"}`, "NOW()")

	desc := e.do(t, "GET", "/v1/describe", nil).requireStatus(t, 200)
	routes, _ := desc.body["extension_routes"].([]any)
	if len(routes) == 0 {
		t.Fatalf("describe returned no extension_routes: %s", desc.raw)
	}
	var decl map[string]any
	for _, r := range routes {
		m := r.(map[string]any)
		if m["id"] == "notifications.list" {
			decl = m
			break
		}
	}
	if decl == nil {
		t.Fatalf("describe does not declare notifications.list — a user-actionable event the module records "+
			"must be DISCOVERABLE, not merely reachable by someone who read the source: %s", desc.raw)
	}
	if decl["auth"] != "platform_service_token" || decl["scope"] != "user" {
		t.Fatalf("notifications.list declared as auth=%v scope=%v, want platform_service_token/user", decl["auth"], decl["scope"])
	}
	ref, _ := decl["response_schema_ref"].(string)
	if ref != "#/schemas/notifications_list_response" {
		t.Fatalf("notifications.list response_schema_ref=%q, want #/schemas/notifications_list_response — a route "+
			"whose ref points nowhere is not discoverable", ref)
	}

	// Call it with ONLY what describe said, then check the body against the declared fragment.
	method, _ := decl["method"].(string)
	path, _ := decl["path"].(string)
	got := e.do(t, method, path+"?user_id="+userID, nil).requireStatus(t, 200)
	items := notificationItems(t, got)
	if len(items) != 1 {
		t.Fatalf("driving the route from describe's own method/path returned %d rows, want 1: %s", len(items), got.raw)
	}

	schemas, _ := desc.body["schemas"].(map[string]any)
	frag, _ := schemas["notifications_list_response"].(map[string]any)
	if frag == nil {
		t.Fatalf("describe's schemas has no notifications_list_response — the ref points at nothing: %s", desc.raw)
	}
	props, _ := frag["properties"].(map[string]any)
	itemSchema, _ := props["items"].(map[string]any)
	itemProps, _ := itemSchema["items"].(map[string]any)
	declaredFields, _ := itemProps["properties"].(map[string]any)
	if len(declaredFields) == 0 {
		t.Fatalf("notifications_list_response declares no item fields: %s", desc.raw)
	}
	// Every declared field is really present, AND every returned field is really declared. Both
	// directions: a declaration listing a field the handler drops is a lie a client codes against,
	// and a field returned but undeclared is undiscoverable.
	for name := range declaredFields {
		if _, ok := items[0][name]; !ok {
			t.Fatalf("describe declares item field %q but the response has no such key (%v) — a caller building "+
				"off describe would code against a field that never arrives: %s", name, keysOfAny(items[0]), got.raw)
		}
	}
	for name := range items[0] {
		if _, ok := declaredFields[name]; !ok {
			t.Fatalf("the response returns item field %q, which describe does NOT declare — undeclared output is "+
				"undiscoverable, and for this route (payload exposure) it is also unreviewed: %s", name, got.raw)
		}
	}
	// The declared `state` enum must cover what the DB type can actually hold, or a client that
	// switch-defaults to an error breaks on a legitimate row.
	stateSchema, _ := declaredFields["state"].(map[string]any)
	declaredStates := map[string]bool{}
	if raw, ok := stateSchema["enum"].([]any); ok {
		for _, v := range raw {
			s, _ := v.(string)
			declaredStates[s] = true
		}
	}
	var dbStates []string
	rows, err := e.admin.Query(bgctx(),
		`SELECT unnest(enum_range(NULL::send_byo.notification_state))::text`)
	if err != nil {
		t.Fatalf("read notification_state enum: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan enum value: %v", err)
		}
		dbStates = append(dbStates, s)
	}
	if len(dbStates) == 0 {
		t.Fatal("send_byo.notification_state has no values — this check would be vacuous")
	}
	for _, s := range dbStates {
		if !declaredStates[s] {
			t.Fatalf("send_byo.notification_state can hold %q but describe's state enum does not declare it "+
				"(declared: %v, db: %v) — a client that hard-fails on an unknown value breaks on a legitimate row. "+
				"This is the coupling send_byo_0047 (which ADDED 'suppressed') would otherwise have broken silently.",
				s, keysOf(declaredStates), dbStates)
		}
	}
}

func keysOfAny(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestNotificationsRead_PayloadIsUnwrappedJSON — payload must be an OBJECT in the response, not a
// JSON string containing JSON. json.RawMessage does that; a plain []byte or string field would emit
// base64 or a quoted blob and force every client to double-decode. Cheap to get wrong, invisible in
// a status-code test, and it would make the field useless.
func TestNotificationsRead_PayloadIsUnwrappedJSON(t *testing.T) {
	e := newTestEnv(t, "notifreadpayload")
	userID := e.seedUser(t, "notifreadpayload@example.com")
	subject := e.seedUser(t, "notifreadpayload-subject@example.com")
	e.seedNotificationAt(t, userID, "account_quarantined", "account", subject, "critical",
		"payload:"+subject, "delivered", "NOW()", "NULL",
		`{"account_id":"`+subject+`","reason":"consecutive_failures"}`, "NOW()")

	got := e.do(t, "GET", "/v1/send-byo/notifications?user_id="+userID, nil).requireStatus(t, 200)
	// Assert on the RAW body, not the decoded map: `"payload":{` proves an inline object, whereas a
	// decoded map[string]any would also be produced from a doubly-encoded string by a lenient reader.
	if !strings.Contains(got.raw, `"payload":{`) {
		t.Fatalf("payload is not serialized as an inline JSON object (want `\"payload\":{`), so every client "+
			"would have to double-decode it: %s", got.raw)
	}
	var probe struct {
		Items []struct {
			Payload map[string]any `json:"payload"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(got.raw), &probe); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(probe.Items) != 1 || probe.Items[0].Payload["reason"] != "consecutive_failures" {
		t.Fatalf("payload did not decode into an object with the seeded keys: %s", got.raw)
	}
}
