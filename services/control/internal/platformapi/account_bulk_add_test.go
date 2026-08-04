package platformapi

// account_bulk_add_test.go — Module 3 Phase 3, C4, §8.4 T4 dispositive part 1 (partial success).
// Real Postgres, real HTTP against the actual production Server (no mocks, CLAUDE.md Rule 3).
// Part 2 (pacing, observable at a real SMTP sink) is the Node smtp.test.mjs suite — a Go handler
// test with a nil verifier cannot observe worker-side connection timestamps (§8.4's anti-vacuity
// note: the SINK must record connection timestamps, not the module's own log lines).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// bulkRowJSON is the decoded shape of one entry in the bulk-add response's results[] array.
type bulkRowJSON struct {
	Row          int    `json:"row"`
	Status       string `json:"status"`
	AccountID    string `json:"account_id"`
	Verification string `json:"verification"`
	Code         string `json:"code"`
}

func decodeBulkResults(t *testing.T, raw string) []bulkRowJSON {
	t.Helper()
	var env struct {
		Results []bulkRowJSON `json:"results"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("decode bulk response: %v; raw=%s", err, raw)
	}
	return env.Results
}

// TestBulkAdd_PartialSuccessByRowIndex (T4 part 1, §8.4) — a mixed 5-row batch: valid, a duplicate
// of row 1, a malformed port, one that crosses the cap, and one more valid row past the cap gets
// re-opened. Asserts the exact per-row outcome BY ROW INDEX (1-based) and that the DB contains
// EXACTLY the rows that reported "created" — no row silently skipped, none silently over-applied.
func TestBulkAdd_PartialSuccessByRowIndex(t *testing.T) {
	e := newTestEnv(t, "bulkpartial")
	e.setConfigInt(t, "account_count_cap_per_user", 2)
	userID := e.seedUser(t, "bulkpartial@example.com")

	body := map[string]any{
		"user_id": userID,
		"accounts": []map[string]any{
			{ // row 1: valid — fills cap slot 1
				"smtp_host": "smtp.bulk1.com", "smtp_username": "bulk-user-1",
				"smtp_secret": "s3cr3t-1", "from_address": "sender@ourdomain.com",
			},
			{ // row 2: duplicate of row 1's (host, username) for the SAME user — rejected, conflict
				"smtp_host": "smtp.bulk1.com", "smtp_username": "bulk-user-1",
				"smtp_secret": "s3cr3t-1dup", "from_address": "sender@ourdomain.com",
			},
			{ // row 3: malformed port — rejected, validation_failed, never reaches the DB
				"smtp_host": "smtp.bulk3.com", "smtp_username": "bulk-user-3", "smtp_port": 99999,
				"smtp_secret": "s3cr3t-3", "from_address": "sender@ourdomain.com",
			},
			{ // row 4: valid — fills cap slot 2 (the last one at cap=2)
				"smtp_host": "smtp.bulk4.com", "smtp_username": "bulk-user-4",
				"smtp_secret": "s3cr3t-4", "from_address": "sender@ourdomain.com",
			},
			{ // row 5: valid on its own, but the cap is now exhausted by rows 1+4 — rejected
				"smtp_host": "smtp.bulk5.com", "smtp_username": "bulk-user-5",
				"smtp_secret": "s3cr3t-5", "from_address": "sender@ourdomain.com",
			},
		},
	}

	resp := e.do(t, "POST", "/v1/send-byo/accounts/bulk", body).requireStatus(t, 200)
	rows := decodeBulkResults(t, resp.raw)
	if len(rows) != 5 {
		t.Fatalf("got %d results, want 5; raw=%s", len(rows), resp.raw)
	}

	want := []struct {
		row    int
		status string
		code   string // only checked for "rejected" rows
	}{
		{1, "created", ""},
		{2, "rejected", "duplicate_account_for_user"},
		{3, "rejected", "validation_failed"},
		{4, "created", ""},
		{5, "rejected", "account_cap_reached"},
	}
	for i, w := range want {
		got := rows[i]
		if got.Row != w.row {
			t.Fatalf("row[%d].Row=%d, want %d", i, got.Row, w.row)
		}
		if got.Status != w.status {
			t.Fatalf("row %d: status=%q, want %q (raw=%s)", w.row, got.Status, w.status, resp.raw)
		}
		if w.status == "rejected" && got.Code != w.code {
			t.Fatalf("row %d: code=%q, want %q", w.row, got.Code, w.code)
		}
		if w.status == "created" && got.AccountID == "" {
			t.Fatalf("row %d: status=created but no account_id", w.row)
		}
	}

	// The DB contains EXACTLY the rows that reported "created" — rows 1 and 4, nothing else. A
	// row-4 create succeeding despite row-2/row-3's earlier rejections is the "no transaction
	// spans the batch" property (§4.2): a 23505/validation failure on an earlier row must not
	// roll back a later one.
	if n := e.accountCountForUserDirect(t, userID); n != 2 {
		t.Fatalf("account count for user after mixed batch = %d, want 2 (exactly rows 1+4)", n)
	}
	var hosts []string
	dbRows, err := e.admin.Query(context.Background(),
		`SELECT smtp_host FROM send_byo.accounts WHERE user_id=$1 ORDER BY smtp_host`, userID)
	if err != nil {
		t.Fatalf("query hosts: %v", err)
	}
	defer dbRows.Close()
	for dbRows.Next() {
		var h string
		if err := dbRows.Scan(&h); err != nil {
			t.Fatalf("scan host: %v", err)
		}
		hosts = append(hosts, h)
	}
	if len(hosts) != 2 || hosts[0] != "smtp.bulk1.com" || hosts[1] != "smtp.bulk4.com" {
		t.Fatalf("hosts in DB = %v, want exactly [smtp.bulk1.com smtp.bulk4.com]", hosts)
	}
}

// TestBulkAdd_RowCountBound (M3-P3-OD-8/19) — a batch exceeding bulk_add_max_rows is rejected
// WHOLE, before any row is processed: 422 batch_too_large, and NO account rows created at all
// (a partial-success response never applies to an over-limit batch — checked first, §4.2).
// Control: a batch AT the bound (not over it) is accepted and processed normally.
func TestBulkAdd_RowCountBound(t *testing.T) {
	e := newTestEnv(t, "bulkmaxrows")
	e.setConfigInt(t, "bulk_add_max_rows", 2)
	e.setConfigInt(t, "account_count_cap_per_user", 100) // cap must not be what rejects this batch
	userID := e.seedUser(t, "bulkmaxrows@example.com")

	overBody := map[string]any{
		"user_id": userID,
		"accounts": []map[string]any{
			{"smtp_host": "smtp.over1.com", "smtp_username": "over-1", "smtp_secret": "s1", "from_address": "sender@ourdomain.com"},
			{"smtp_host": "smtp.over2.com", "smtp_username": "over-2", "smtp_secret": "s2", "from_address": "sender@ourdomain.com"},
			{"smtp_host": "smtp.over3.com", "smtp_username": "over-3", "smtp_secret": "s3", "from_address": "sender@ourdomain.com"},
		},
	}
	over := e.do(t, "POST", "/v1/send-byo/accounts/bulk", overBody).requireStatus(t, 422)
	if over.errorCode() != codeUnprocessable {
		t.Fatalf("over-bound batch: code=%s, want unprocessable", over.errorCode())
	}
	if n := e.accountCountForUserDirect(t, userID); n != 0 {
		t.Fatalf("account count after a REJECTED over-bound batch = %d, want 0 (whole-batch reject, no partial apply)", n)
	}

	// Control: a batch AT the bound (2 rows, bound=2) is accepted and both rows are processed.
	atBoundBody := map[string]any{
		"user_id": userID,
		"accounts": []map[string]any{
			{"smtp_host": "smtp.at1.com", "smtp_username": "at-1", "smtp_secret": "s1", "from_address": "sender@ourdomain.com"},
			{"smtp_host": "smtp.at2.com", "smtp_username": "at-2", "smtp_secret": "s2", "from_address": "sender@ourdomain.com"},
		},
	}
	atBound := e.do(t, "POST", "/v1/send-byo/accounts/bulk", atBoundBody).requireStatus(t, 200)
	rows := decodeBulkResults(t, atBound.raw)
	if len(rows) != 2 || rows[0].Status != "created" || rows[1].Status != "created" {
		t.Fatalf("at-bound batch: got %+v, want both rows created", rows)
	}
}

// TestBulkAdd_RequiresTenantSelector — a missing/malformed user_id is a clean 400
// validation_failed, matching every other route's tenancy gate (§1.1).
func TestBulkAdd_RequiresTenantSelector(t *testing.T) {
	e := newTestEnv(t, "bulknouser")
	noUser := e.do(t, "POST", "/v1/send-byo/accounts/bulk", map[string]any{
		"accounts": []map[string]any{{"smtp_host": "h", "smtp_username": "u", "smtp_secret": "s", "from_address": "a@b.com"}},
	}).requireStatus(t, 400)
	if noUser.errorCode() != codeValidationFailed {
		t.Fatalf("no user_id: code=%s, want validation_failed", noUser.errorCode())
	}

	empty := e.do(t, "POST", "/v1/send-byo/accounts/bulk", map[string]any{
		"user_id": e.seedUser(t, "bulknouser@example.com"), "accounts": []map[string]any{},
	}).requireStatus(t, 400)
	if empty.errorCode() != codeValidationFailed {
		t.Fatalf("empty accounts: code=%s, want validation_failed", empty.errorCode())
	}
}

// TestDescribe_BulkAddWarningContract (§4.3) — describe declares the credentials-in-a-CSV UI
// copy contract as a required, versioned field: warning_code + warning_text. The module has no
// UI, so this is the only enforceable place the contract can live; a UI that never calls describe
// has no way to learn it must show this warning at all.
func TestDescribe_BulkAddWarningContract(t *testing.T) {
	e := newTestEnv(t, "describebulk")
	resp := e.do(t, "GET", "/v1/describe", nil).requireStatus(t, 200)
	bulkAdd, ok := resp.body["bulk_add"].(map[string]any)
	if !ok {
		t.Fatalf("describe response has no bulk_add object: %s", resp.raw)
	}
	if bulkAdd["warning_code"] != "credentials_in_plaintext_file" {
		t.Fatalf("bulk_add.warning_code=%v, want credentials_in_plaintext_file", bulkAdd["warning_code"])
	}
	text, _ := bulkAdd["warning_text"].(string)
	if text == "" {
		t.Fatalf("bulk_add.warning_text is empty")
	}
	// The four required points (§4.3): plaintext passwords, on-disk persistence, delete-after,
	// and "not a backup." Checked as substrings rather than an exact string match — the UI's own
	// wording may differ, but the module's declared baseline must convey all four itself.
	for _, must := range []string{"plaintext", "disk", "delete", "not a backup"} {
		if !strings.Contains(strings.ToLower(text), must) {
			t.Fatalf("bulk_add.warning_text missing required point %q: %s", must, text)
		}
	}
}
