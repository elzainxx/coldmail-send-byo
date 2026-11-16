package adminapi

// user_suspension_read_test.go — the P2 close-out, owner ruling C2: GET /v1/admin/users/{id}/suspension
// and the monotonic `version` (send_byo_0054). Real Postgres, real HTTP against the actual production
// Server, no mocks (CLAUDE.md Rule 3).
//
// THE SHAPE OF EVERY ASSERTION HERE. Each property is checked by ONE shared checker
// (suspensionViolations / versionSequenceViolations) and each checker has a PAIRED CONTROL that feeds it
// a deliberately wrong answer and requires it to object — so a green run means the checker can fail,
// not merely that the route happened to agree with it (CLAUDE.md: "a test that cannot fail is not a
// test"). The concurrency test is paired with a run of the SAME forced interleave against the
// suspend statement with its guard removed, which must produce the opposite outcome.
//
// FAIL-VERIFY (the fault proof, ci/faults/p2c/m3-suspension-read in the platform repository): delete
// the `version = user_suspensions.version + 1` assignment from suspendUpsertSQL and the version
// assertions in TestUserSuspensionRead_ReportsEveryStateAndVersion go red naming the stale version.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// suspensionWant is what one read of the route is expected to say.
type suspensionWant struct {
	suspended       bool
	hasSuspendedAt  bool
	hasReinstatedAt bool
	version         int64
}

// suspensionViolations is the CHECKER every read assertion in this file goes through. It returns one
// line per way raw departs from want: a key set that is not EXACTLY the five documented keys (so a
// leaked reason text or operator identity is a violation, not an extra the test ignores), a wrong
// type, a timestamp that is not RFC 3339 UTC, a wrong flag, a wrong version. An empty result means
// the body is exactly what the contract promises.
func suspensionViolations(raw []byte, wantUserID string, want suspensionWant) []string {
	var v []string
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(raw, &generic); err != nil {
		return []string{"body is not a JSON object: " + err.Error()}
	}
	wantKeys := map[string]bool{"user_id": true, "suspended": true, "suspended_at": true, "reinstated_at": true, "version": true}
	for k := range generic {
		if !wantKeys[k] {
			v = append(v, "unexpected key "+strconv.Quote(k)+" (the route returns exactly five keys: no reason text, no operator identity)")
		}
	}
	for k := range wantKeys {
		if _, ok := generic[k]; !ok {
			v = append(v, "missing key "+strconv.Quote(k))
		}
	}
	if len(v) > 0 {
		sort.Strings(v)
		return v
	}

	var body struct {
		UserID       string  `json:"user_id"`
		Suspended    *bool   `json:"suspended"`
		SuspendedAt  *string `json:"suspended_at"`
		ReinstatedAt *string `json:"reinstated_at"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return []string{"body does not decode into the documented types: " + err.Error()}
	}
	if body.UserID != wantUserID {
		v = append(v, fmt.Sprintf("user_id=%q, want %q", body.UserID, wantUserID))
	}
	switch {
	case body.Suspended == nil:
		v = append(v, "suspended is null, want a boolean")
	case *body.Suspended != want.suspended:
		v = append(v, fmt.Sprintf("suspended=%v, want %v", *body.Suspended, want.suspended))
	}
	v = append(v, timestampViolations("suspended_at", body.SuspendedAt, want.hasSuspendedAt)...)
	v = append(v, timestampViolations("reinstated_at", body.ReinstatedAt, want.hasReinstatedAt)...)

	// version: a bare JSON integer. Parsing the RAW token (not a decoded float) is what rejects a
	// quoted "1", a 1.5 and a 1e3, none of which is "an integer" in the contract's sense.
	ver, err := strconv.ParseInt(string(generic["version"]), 10, 64)
	switch {
	case err != nil:
		v = append(v, fmt.Sprintf("version=%s is not a bare JSON integer", generic["version"]))
	case ver != want.version:
		v = append(v, fmt.Sprintf("version=%d, want %d", ver, want.version))
	}
	return v
}

// timestampViolations checks one nullable timestamp field: null exactly when it is not expected, and
// when present an RFC 3339 instant written in UTC ("Z").
func timestampViolations(field string, got *string, want bool) []string {
	if !want {
		if got != nil {
			return []string{fmt.Sprintf("%s=%q, want null", field, *got)}
		}
		return nil
	}
	if got == nil {
		return []string{field + " is null, want a timestamp"}
	}
	if _, err := time.Parse(time.RFC3339Nano, *got); err != nil {
		return []string{fmt.Sprintf("%s=%q is not RFC 3339: %v", field, *got, err)}
	}
	if !strings.HasSuffix(*got, "Z") {
		return []string{fmt.Sprintf("%s=%q is not written in UTC (no trailing Z)", field, *got)}
	}
	return nil
}

// versionSequenceViolations returns one line per place a sequence of OBSERVED versions goes down. It
// is deliberately only about direction: a reader can legitimately see the same version twice, and
// sees fewer distinct values than the writer produced.
func versionSequenceViolations(seq []int64) []string {
	var v []string
	for i := 1; i < len(seq); i++ {
		if seq[i] < seq[i-1] {
			v = append(v, fmt.Sprintf("version went DOWN at observation %d: %d after %d", i, seq[i], seq[i-1]))
		}
	}
	return v
}

// TestUserSuspensionRead_CheckerObjectsToEveryWrongBody is the paired control for every read
// assertion below: the same checker, fed one wrong body per way the route could be wrong, must object
// to each — and must accept the one right body. Without this a checker that returned nil for
// everything would make the whole file green.
func TestUserSuspensionRead_CheckerObjectsToEveryWrongBody(t *testing.T) {
	const uid = "11111111-2222-3333-4444-555555555555"
	want := suspensionWant{suspended: true, hasSuspendedAt: true, version: 1}
	good := `{"user_id":"` + uid + `","suspended":true,"suspended_at":"2026-01-01T12:00:00.123456Z","reinstated_at":null,"version":1}`
	if v := suspensionViolations([]byte(good), uid, want); len(v) != 0 {
		t.Fatalf("control premise: the right body was rejected: %v", v)
	}
	wrong := map[string]string{
		"stale version (the handler stopped incrementing)": strings.Replace(good, `"version":1`, `"version":0`, 1),
		"version ahead of the state":                       strings.Replace(good, `"version":1`, `"version":2`, 1),
		"version as a quoted string":                       strings.Replace(good, `"version":1`, `"version":"1"`, 1),
		"version as a fraction":                            strings.Replace(good, `"version":1`, `"version":1.5`, 1),
		"flag flipped":                                     strings.Replace(good, `"suspended":true`, `"suspended":false`, 1),
		"suspended_at missing its value":                   strings.Replace(good, `"2026-01-01T12:00:00.123456Z"`, `null`, 1),
		"reinstated_at present when never reinstated":      strings.Replace(good, `"reinstated_at":null`, `"reinstated_at":"2026-01-01T12:00:01Z"`, 1),
		"timestamp not RFC 3339":                           strings.Replace(good, `2026-01-01T12:00:00.123456Z`, `yesterday`, 1),
		"timestamp not in UTC":                             strings.Replace(good, `2026-01-01T12:00:00.123456Z`, `2026-01-01T14:00:00.123456+02:00`, 1),
		"wrong user_id":                                    strings.Replace(good, uid, "99999999-2222-3333-4444-555555555555", 1),
		"a leaked reason text":                             strings.Replace(good, `"version":1`, `"version":1,"reason":"operator"`, 1),
		"a leaked operator identity":                       strings.Replace(good, `"version":1`, `"version":1,"updated_by":"someone"`, 1),
		"a missing key":                                    strings.Replace(good, `"reinstated_at":null,`, ``, 1),
		"not an object":                                    `[]`,
	}
	for name, body := range wrong {
		if body == good {
			t.Fatalf("control case %q did not change the body — the mutation did not apply", name)
		}
		if v := suspensionViolations([]byte(body), uid, want); len(v) == 0 {
			t.Errorf("the checker ACCEPTED a wrong body (%s): %s", name, body)
		}
	}

	// And the sequence checker: a decrease is a violation, a plateau and a rise are not.
	if v := versionSequenceViolations([]int64{0, 1, 2, 1}); len(v) == 0 {
		t.Error("versionSequenceViolations accepted a sequence that goes down (0,1,2,1)")
	}
	if v := versionSequenceViolations([]int64{2, 1}); len(v) == 0 {
		t.Error("versionSequenceViolations accepted a sequence that goes down (2,1)")
	}
	if v := versionSequenceViolations([]int64{0, 0, 1, 1, 2, 5}); len(v) != 0 {
		t.Errorf("versionSequenceViolations rejected a non-decreasing sequence: %v", v)
	}
}

// readSuspension reads the route as the operator and returns the response.
func (e *testEnv) readSuspension(t *testing.T, userID string) resp {
	t.Helper()
	return e.do(t, "GET", "/v1/admin/users/"+userID+"/suspension", nil).requireStatus(t, 200)
}

// assertSuspension fails with every violation, naming the step and printing the body.
func assertSuspension(t *testing.T, step, userID string, r resp, want suspensionWant) {
	t.Helper()
	if v := suspensionViolations(r.raw, userID, want); len(v) != 0 {
		t.Fatalf("%s: %s\n  body: %s", step, strings.Join(v, "; "), r.raw)
	}
}

// assertSuspensionMatchesTable cross-checks a response against the table itself (the admin connection
// is ground truth), so the route is proven to REPORT the stored row, not merely to be self-consistent.
func (e *testEnv) assertSuspensionMatchesTable(t *testing.T, userID string, r resp) {
	t.Helper()
	var suspended bool
	var suspendedAt, reinstatedAt *time.Time
	var version int64
	err := e.admin.QueryRow(context.Background(),
		`SELECT suspended, suspended_at, reinstated_at, version FROM send_byo.user_suspensions WHERE user_id=$1`, userID).
		Scan(&suspended, &suspendedAt, &reinstatedAt, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("the table has no row for %s but the route answered: %s", userID, r.raw)
	}
	if err != nil {
		t.Fatalf("read the table: %v", err)
	}
	var body struct {
		Suspended    bool    `json:"suspended"`
		SuspendedAt  *string `json:"suspended_at"`
		ReinstatedAt *string `json:"reinstated_at"`
		Version      int64   `json:"version"`
	}
	if err := json.Unmarshal(r.raw, &body); err != nil {
		t.Fatalf("decode: %v: %s", err, r.raw)
	}
	if body.Suspended != suspended || body.Version != version {
		t.Fatalf("route says suspended=%v version=%d, table says suspended=%v version=%d", body.Suspended, body.Version, suspended, version)
	}
	sameInstant := func(field string, gotStr *string, tableVal *time.Time) {
		if (gotStr == nil) != (tableVal == nil) {
			t.Fatalf("%s: route=%v table=%v — one is null and the other is not", field, gotStr, tableVal)
		}
		if gotStr == nil {
			return
		}
		got, err := time.Parse(time.RFC3339Nano, *gotStr)
		if err != nil {
			t.Fatalf("%s=%q: %v", field, *gotStr, err)
		}
		if !got.Equal(*tableVal) {
			t.Fatalf("%s: route=%s table=%s — the route does not report the stored instant", field, got.Format(time.RFC3339Nano), tableVal.UTC().Format(time.RFC3339Nano))
		}
	}
	sameInstant("suspended_at", body.SuspendedAt, suspendedAt)
	sameInstant("reinstated_at", body.ReinstatedAt, reinstatedAt)
}

// TestUserSuspensionRead_ReportsEveryStateAndVersion — the exit tests for the four states, in order:
// never suspended (false, both timestamps null, version 0); after suspend (true, suspended_at,
// version 1); after unsuspend (false, reinstated_at, version 2); after a re-suspend (true, version
// 3). Reads write nothing. A SECOND user, never touched, is the control that the answers are per
// user and not a constant: it keeps reading the never-suspended answer throughout.
func TestUserSuspensionRead_ReportsEveryStateAndVersion(t *testing.T) {
	e := newTestEnv(t, "susprd_states")
	userID := e.seedUser(t, "susprd-states@example.com")
	other := e.seedUser(t, "susprd-states-other@example.com")
	base := "/v1/admin/users/" + userID

	// 1. Never suspended: a KNOWN user in good standing is a 200, version 0, and the read created no row.
	assertSuspension(t, "never suspended", userID, e.readSuspension(t, userID), suspensionWant{})
	var rows, events int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT (SELECT count(*) FROM send_byo.user_suspensions WHERE user_id=$1),
		        (SELECT count(*) FROM send_byo.account_events WHERE user_id=$1)`, userID).Scan(&rows, &events); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 0 || events != 0 {
		t.Fatalf("after a read of a never-suspended user: user_suspensions rows=%d account_events rows=%d, want 0 and 0 — a read must write nothing", rows, events)
	}

	// 2. Suspend (with an operator note that must never come back out).
	e.do(t, "POST", base+"/suspend", map[string]any{"reason": "operator note do-not-leak"}).requireStatus(t, 200)
	r := e.readSuspension(t, userID)
	assertSuspension(t, "after suspend", userID, r, suspensionWant{suspended: true, hasSuspendedAt: true, version: 1})
	e.assertSuspensionMatchesTable(t, userID, r)
	if strings.Contains(string(r.raw), "do-not-leak") || strings.Contains(string(r.raw), "operator") {
		t.Fatalf("the read leaked the operator's note or the stored reason enum: %s", r.raw)
	}

	// The control: another user is untouched by that suspend.
	assertSuspension(t, "other user, after the first user's suspend", other, e.readSuspension(t, other), suspensionWant{})

	// 3. Unsuspend: false, reinstated_at set, version 2. suspended_at keeps the instant of the suspension
	// that just ended (the column is not cleared), so a caller reads "suspended=false" for the state.
	e.do(t, "POST", base+"/unsuspend", nil).requireStatus(t, 200)
	r = e.readSuspension(t, userID)
	assertSuspension(t, "after unsuspend", userID, r, suspensionWant{suspended: false, hasSuspendedAt: true, hasReinstatedAt: true, version: 2})
	e.assertSuspensionMatchesTable(t, userID, r)

	// 4. Suspend again: back to true, version 3 (a transition counter, not a boolean's history).
	e.do(t, "POST", base+"/suspend", nil).requireStatus(t, 200)
	r = e.readSuspension(t, userID)
	assertSuspension(t, "after a re-suspend", userID, r, suspensionWant{suspended: true, hasSuspendedAt: true, hasReinstatedAt: true, version: 3})
	e.assertSuspensionMatchesTable(t, userID, r)

	assertSuspension(t, "other user, at the end", other, e.readSuspension(t, other), suspensionWant{})
}

// TestUserSuspensionRead_VersionNeverDecreasesAndMovesOnlyOnATransition — a fixed sequence of calls
// including every REFUSED one: a refused call (422) changes no flag, so it must not move the version.
// The observed reads must equal the expected sequence EXACTLY, and the same direction checker the
// control above exercises must find no decrease.
func TestUserSuspensionRead_VersionNeverDecreasesAndMovesOnlyOnATransition(t *testing.T) {
	e := newTestEnv(t, "susprd_seq")
	userID := e.seedUser(t, "susprd-seq@example.com")
	base := "/v1/admin/users/" + userID

	steps := []struct {
		op          string
		wantStatus  int
		wantVersion int64
		wantFlag    bool
	}{
		{"suspend", 200, 1, true},
		{"suspend", 422, 1, true}, // already suspended: refused, the counter must not move
		{"unsuspend", 200, 2, false},
		{"unsuspend", 422, 2, false}, // not suspended: refused, the counter must not move
		{"suspend", 200, 3, true},
		{"unsuspend", 200, 4, false},
		{"suspend", 200, 5, true},
		{"suspend", 422, 5, true},
	}
	observed := []int64{}
	for i, s := range steps {
		got := e.do(t, "POST", base+"/"+s.op, nil)
		if got.status != s.wantStatus {
			t.Fatalf("step %d (%s): status=%d, want %d; body=%s", i, s.op, got.status, s.wantStatus, got.raw)
		}
		r := e.readSuspension(t, userID)
		var body struct {
			Version int64 `json:"version"`
		}
		if err := json.Unmarshal(r.raw, &body); err != nil {
			t.Fatalf("step %d: decode: %v", i, err)
		}
		observed = append(observed, body.Version)
		if body.Version != s.wantVersion {
			t.Fatalf("step %d (%s -> %d): version=%d, want %d; body=%s", i, s.op, got.status, body.Version, s.wantVersion, r.raw)
		}
		assertSuspension(t, fmt.Sprintf("step %d (%s)", i, s.op), userID, r,
			suspensionWant{suspended: s.wantFlag, hasSuspendedAt: true, hasReinstatedAt: i >= 2, version: s.wantVersion})
	}
	if v := versionSequenceViolations(observed); len(v) != 0 {
		t.Fatalf("observed versions %v: %v", observed, v)
	}
}

// goDo is do without t.Fatalf: it is safe to call from a goroutine other than the test's own.
func (e *testEnv) goDo(method, path string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(context.Background(), method, e.baseURL+path, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+e.adminKey)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	return res.StatusCode, raw, err
}

// TestUserSuspensionRead_VersionNeverDecreasesUnderConcurrentReads — a reader polls the route in a
// second goroutine for the whole time a writer toggles the user through many transitions. Every
// version the reader sees must be >= the one before it ("a read after a write can never show an older
// version"), and the writer's last write must be what the final read shows.
func TestUserSuspensionRead_VersionNeverDecreasesUnderConcurrentReads(t *testing.T) {
	e := newTestEnv(t, "susprd_reader")
	userID := e.seedUser(t, "susprd-reader@example.com")
	base := "/v1/admin/users/" + userID

	var mu sync.Mutex
	var seen []int64
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			status, raw, err := e.goDo("GET", base+"/suspension")
			if err != nil || status != 200 {
				done <- fmt.Errorf("reader: status=%d err=%v body=%s", status, err, raw)
				return
			}
			var body struct {
				Version int64 `json:"version"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				done <- fmt.Errorf("reader: decode: %v: %s", err, raw)
				return
			}
			mu.Lock()
			seen = append(seen, body.Version)
			mu.Unlock()
		}
	}()

	const cycles = 8
	for i := 0; i < cycles; i++ {
		e.do(t, "POST", base+"/suspend", nil).requireStatus(t, 200)
		e.do(t, "POST", base+"/unsuspend", nil).requireStatus(t, 200)
	}
	close(stop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 3 {
		t.Fatalf("the concurrent reader made only %d observation(s) — too few to say anything about ordering (vacuous)", len(seen))
	}
	if v := versionSequenceViolations(seen); len(v) != 0 {
		t.Fatalf("a concurrent reader saw the version go down across %d writes: %v (observed %v)", 2*cycles, v, seen)
	}
	final := e.readSuspension(t, userID)
	assertSuspension(t, "final read", userID, final, suspensionWant{suspended: false, hasSuspendedAt: true, hasReinstatedAt: true, version: 2 * cycles})
}

// raceTwo holds a SHARE ROW EXCLUSIVE lock on send_byo.user_suspensions (which conflicts with the
// ROW EXCLUSIVE lock any INSERT/UPDATE needs), fires fire() from two goroutines, PROVES BOTH are
// genuinely blocked on it (a scoped pg_stat_activity count — CLAUDE.md Rule 10 and its Phase 8
// addendum: this database, Lock-waiting, active, and the statement's own text), checks neither has
// completed, releases the lock, and returns the two results sorted. The interleave is forced, not hoped
// for: both statements are parked at the same point and released together.
func (e *testEnv) raceTwo(t *testing.T, fire func() (int, error)) []int {
	t.Helper()
	ctx := context.Background()
	lockConn := connect(t, e.cloneDSN)
	lockTx, err := lockConn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock tx: %v", err)
	}
	defer lockTx.Rollback(ctx) //nolint:errcheck — a no-op after the Commit below
	if _, err := lockTx.Exec(ctx, `LOCK TABLE send_byo.user_suspensions IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		t.Fatalf("lock the table: %v", err)
	}

	type result struct {
		n   int
		err error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			n, err := fire()
			results <- result{n, err}
		}()
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		var blocked int
		if err := e.admin.QueryRow(ctx,
			`SELECT count(*) FROM pg_stat_activity
			  WHERE datname = current_database()
			    AND wait_event_type = 'Lock' AND state = 'active'
			    AND query ILIKE '%INSERT INTO send_byo.user_suspensions%'`).Scan(&blocked); err != nil {
			t.Fatalf("read pg_stat_activity: %v", err)
		}
		if blocked == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of 2 backends are blocked on the table lock — the two statements are not genuinely contending (vacuous)", blocked)
		}
		time.Sleep(50 * time.Millisecond)
	}
	select {
	case r := <-results:
		t.Fatalf("a statement completed (n=%d err=%v) while the table lock was held — the interleave did not occur, this test proves nothing", r.n, r.err)
	default:
	}

	if err := lockTx.Commit(ctx); err != nil {
		t.Fatalf("release the table lock: %v", err)
	}
	out := make([]int, 0, 2)
	for i := 0; i < 2; i++ {
		select {
		case r := <-results:
			if r.err != nil {
				t.Fatalf("a raced statement failed: %v", r.err)
			}
			out = append(out, r.n)
		case <-time.After(20 * time.Second):
			t.Fatal("a raced statement never completed after the lock was released — the two are wedged")
		}
	}
	sort.Ints(out)
	return out
}

// TestUserSuspensionRead_TwoConcurrentSuspendsExactlyOneWinsAndVersionIsOne — two REAL suspend calls
// for a never-suspended user are parked at the same statement and released together: exactly one is
// 200 and the other 422 (not a raw 23505, not two 200s), the version reads 1 (the loser did not bump
// it), and exactly one audit row exists.
func TestUserSuspensionRead_TwoConcurrentSuspendsExactlyOneWinsAndVersionIsOne(t *testing.T) {
	e := newTestEnv(t, "susprd_race")
	userID := e.seedUser(t, "susprd-race@example.com")

	statuses := e.raceTwo(t, func() (int, error) {
		status, _, err := e.goDo("POST", "/v1/admin/users/"+userID+"/suspend")
		return status, err
	})
	if len(statuses) != 2 || statuses[0] != 200 || statuses[1] != 422 {
		t.Fatalf("two concurrent suspends answered %v, want exactly one 200 and one 422", statuses)
	}
	assertSuspension(t, "after the race", userID, e.readSuspension(t, userID),
		suspensionWant{suspended: true, hasSuspendedAt: true, version: 1})
	var audit int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM send_byo.account_events WHERE user_id=$1 AND action='suspend'`, userID).Scan(&audit); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if audit != 1 {
		t.Fatalf("suspend audit rows=%d after two concurrent calls, want exactly 1 (the loser writes none)", audit)
	}
}

// TestUserSuspensionRead_ControlWithoutTheGuardBothSuspendsWinAndVersionIsTwo is the PAIRED CONTROL of
// the test above: the SAME forced interleave, run against the suspend statement with its
// `WHERE ... suspended = FALSE` guard removed, must produce the OPPOSITE outcome — both statements
// report a row changed and the version reads 2. That is what shows the guard is the thing that makes
// "exactly one wins, version 1" true, rather than the table lock or the handler's pre-checks.
func TestUserSuspensionRead_ControlWithoutTheGuardBothSuspendsWinAndVersionIsTwo(t *testing.T) {
	e := newTestEnv(t, "susprd_noguard")
	userID := e.seedUser(t, "susprd-noguard@example.com")
	ctx := context.Background()

	unguarded := strings.Replace(suspendUpsertSQL, "WHERE user_suspensions.suspended = FALSE", "", 1)
	if unguarded == suspendUpsertSQL {
		t.Fatal("the control's mutation did not apply: suspendUpsertSQL no longer ends in its guard, so this control would be running the real statement")
	}
	pool, err := pgxpool.New(ctx, e.moduleDSN())
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	rows := e.raceTwo(t, func() (int, error) {
		tag, err := pool.Exec(ctx, unguarded, userID)
		if err != nil {
			return 0, err
		}
		return int(tag.RowsAffected()), nil
	})
	if len(rows) != 2 || rows[0] != 1 || rows[1] != 1 {
		t.Fatalf("unguarded concurrent suspends affected %v rows, want [1 1] — both 'won', which is exactly what the guard exists to prevent", rows)
	}
	var version int64
	if err := e.admin.QueryRow(ctx, `SELECT version FROM send_byo.user_suspensions WHERE user_id=$1`, userID).Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != 2 {
		t.Fatalf("unguarded concurrent suspends left version=%d, want 2 (one bump per 'winner') — the control must show the double count the guard prevents", version)
	}
}

// TestUserSuspensionRead_AuthMethodAndNotFoundVocabulary — the route's envelope and errors are the
// siblings': a wrong or missing key is 401, a wrong verb 405, a malformed id and an UNKNOWN user are
// both a 404 not_found, and a known user who was never suspended is the 200 control for all of them.
func TestUserSuspensionRead_AuthMethodAndNotFoundVocabulary(t *testing.T) {
	e := newTestEnv(t, "susprd_errs")
	userID := e.seedUser(t, "susprd-errs@example.com")
	good := "/v1/admin/users/" + userID + "/suspension"

	// The control every refusal below is measured against: the right key on a known user reads 200.
	assertSuspension(t, "control read", userID, e.do(t, "GET", good, nil).requireStatus(t, 200), suspensionWant{})

	for _, bearer := range []string{"not-the-operator-key", ""} {
		r := e.doRaw(t, "GET", good, nil, bearer)
		if r.status != 401 || r.errorCode() != codeUnauthenticated {
			t.Fatalf("GET with bearer=%q: status=%d code=%q, want 401 unauthenticated; body=%s", bearer, r.status, r.errorCode(), r.raw)
		}
		if r.errorRequestID() == "" {
			t.Fatalf("the 401 carries no request_id: %s", r.raw)
		}
	}

	unknown := e.do(t, "GET", "/v1/admin/users/00000000-0000-0000-0000-00000000dead/suspension", nil)
	if unknown.status != 404 || unknown.errorCode() != codeNotFound {
		t.Fatalf("GET for an unknown user: status=%d code=%q, want 404 not_found; body=%s", unknown.status, unknown.errorCode(), unknown.raw)
	}
	malformed := e.do(t, "GET", "/v1/admin/users/not-a-uuid/suspension", nil)
	if malformed.status != 404 || malformed.errorCode() != codeNotFound {
		t.Fatalf("GET for a malformed id: status=%d code=%q, want 404 not_found (the siblings' own choice); body=%s", malformed.status, malformed.errorCode(), malformed.raw)
	}
	// Neither 404 echoes the id back or says which of the two it was.
	if unknown.errorMessage() != malformed.errorMessage() {
		t.Fatalf("the unknown-user and malformed-id 404s differ in their message (%q vs %q): the caller should not be able to tell them apart", unknown.errorMessage(), malformed.errorMessage())
	}

	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		r := e.do(t, method, good, nil)
		if r.status != 405 || r.errorCode() != codeMethodNotAllowed {
			t.Fatalf("%s on the read route: status=%d code=%q, want 405 method_not_allowed; body=%s", method, r.status, r.errorCode(), r.raw)
		}
	}
}
