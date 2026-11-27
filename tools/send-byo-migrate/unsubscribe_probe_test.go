package main

// unsubscribe_probe_test.go — the lifecycle test's helpers for send_byo_0060 (the nullable send_byo.jobs.unsubscribe_url column, Phase 5 S4: the source of the
// List-Unsubscribe and List-Unsubscribe-Post headers) and send_byo_0061 (the owner's 30-day campaign_expiry_sec, re-seeded by DELETE + INSERT), plus the
// pre-flight's second required column and the runner's refusal to migrate without it. Real Postgres, never a mock (CLAUDE.md Rule 3). Like 0058's probe it DRIVES
// each property (a refused value, a refused migration) rather than reading a definition back, which would assert the migration's own SQL against itself, and every
// refusal has an accepted twin that differs only in the property under test.

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// unsubscribeProbeBase is a value jobs_unsubscribe_url_check accepts: the control every hostile value below is a one-character-class variation of.
const unsubscribeProbeBase = "https://unsubscribe.example.test/u/AbC-123_xyz"

// assertUnsubscribeMigration is the "after Up" half for 0060. Anti-vacuity for the object, and it DRIVES the CHECK on both sides (a pg_get_constraintdef match would
// assert the migration's SQL against itself).
func assertUnsubscribeMigration(t *testing.T, c *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	// the column exists, is TEXT, nullable, with NO default (so every existing row reads as absent: no headers)
	var dataType, nullable string
	var dflt *string
	if err := c.QueryRow(ctx,
		`SELECT data_type, is_nullable, column_default FROM information_schema.columns
		  WHERE table_schema='send_byo' AND table_name='jobs' AND column_name='unsubscribe_url'`).Scan(&dataType, &nullable, &dflt); err != nil {
		t.Fatalf("after Up: send_byo.jobs.unsubscribe_url missing (send_byo_0060 not applied): %v", err)
	}
	if dataType != "text" || nullable != "YES" || dflt != nil {
		t.Fatalf("after Up: jobs.unsubscribe_url is %s nullable=%s default=%v, want text / YES / no default", dataType, nullable, dflt)
	}
	if !constraintExists(t, c, "jobs", "jobs_unsubscribe_url_check") {
		t.Fatal("after Up: jobs_unsubscribe_url_check is missing (send_byo_0060 did not add the CHECK)")
	}

	// Drive the CHECK in ONE rolled-back transaction. ACCEPTED: NULL, a plain https URL, one with a query, a fragment and a percent escape, and exactly 2001
	// characters (the scheme's 8 plus the platform's 1993). REFUSED (23514 naming the constraint): every property the header cannot carry.
	tx, err := c.Begin(ctx)
	if err != nil {
		t.Fatalf("begin unsubscribe_url probe: %v", err)
	}
	defer tx.Rollback(ctx)
	var userID string
	if err := tx.QueryRow(ctx, `INSERT INTO public.users (email, is_active) VALUES ('unsubscribe-probe@sender.local', true) RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("unsubscribe_url probe: seed user: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('send_byo.actor','system',true)`); err != nil {
		t.Fatalf("unsubscribe_url probe: set actor: %v", err)
	}
	n := 0
	insert := func(link *string) error {
		n++
		label := "unsub-probe-" + strconv.Itoa(n)
		var itemID string
		// the SIX-COLUMN shape of test-send's INSERT (send_byo_0037), with NO unsubscribe_url: it must keep working against a platform table that has the column
		if err := tx.QueryRow(ctx,
			`INSERT INTO public.job_items (campaign_id, user_id, recipient_email, rendered_subject, rendered_body, status)
			 VALUES ($1,$1,$2,'S','<p>b</p>','pending') RETURNING id`, userID, label+"@example.com").Scan(&itemID); err != nil {
			t.Fatalf("unsubscribe_url probe: seed job_item %s: %v", label, err)
		}
		if _, err := tx.Exec(ctx, `SAVEPOINT sp`); err != nil {
			t.Fatalf("savepoint: %v", err)
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO send_byo.jobs (job_item_id, campaign_id, user_id, recipient_email, rendered_subject, rendered_body, unsubscribe_url, state, next_attempt_at)
			 VALUES ($1,$2,$2,$3,'S','<p>b</p>', $4, 'queued', NOW())`, itemID, userID, label+"@example.com", link)
		if err != nil {
			_, _ = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT sp`)
			return err
		}
		_, _ = tx.Exec(ctx, `RELEASE SAVEPOINT sp`)
		return nil
	}
	str := func(s string) *string { return &s }

	accepted := map[string]*string{
		"NULL (the platform wrote none)": nil,
		"a plain https URL":              str(unsubscribeProbeBase),
		"a query, a fragment, a %20":     str(unsubscribeProbeBase + "?a=b&c=%20#frag"),
		"exactly 2001 characters":        str("https://" + strings.Repeat("a", 1993)),
	}
	for label, link := range accepted {
		if err := insert(link); err != nil {
			t.Fatalf("after Up: a job whose unsubscribe_url is %s was REFUSED: %v — the CHECK is tighter than the platform's", label, err)
		}
	}
	refused := map[string]string{
		"an http URL":                         "http://unsubscribe.example.test/u/AbC",
		"a scheme alone":                      "https://",
		"the empty string":                    "",
		"a leading space":                     " " + unsubscribeProbeBase,
		"a space inside":                      unsubscribeProbeBase + "/a b",
		"a TAB":                               unsubscribeProbeBase + "/a\tb",
		"a LF and a second header (Bcc)":      unsubscribeProbeBase + "\nBcc: victim@example.com",
		"a CRLF and a second header (Bcc)":    unsubscribeProbeBase + "\r\nBcc: victim@example.com",
		"a bare CR":                           unsubscribeProbeBase + "\rX",
		"a C0 control (0x01)":                 unsubscribeProbeBase + "/\x01",
		"DEL (0x7f)":                          unsubscribeProbeBase + "/\x7f",
		"a '<' that would close the brackets": unsubscribeProbeBase + "/a<b",
		"a '>' that would close the brackets": unsubscribeProbeBase + "/a>b",
		"2002 characters":                     "https://" + strings.Repeat("a", 1994),
	}
	for label, link := range refused {
		err := insert(str(link))
		if err == nil {
			t.Fatalf("after Up: a job whose unsubscribe_url is %s was ACCEPTED — jobs_unsubscribe_url_check is not enforcing it", label)
		}
		if !strings.Contains(err.Error(), "jobs_unsubscribe_url_check") {
			t.Fatalf("after Up: unsubscribe_url %s failed for the wrong reason (want jobs_unsubscribe_url_check): %v", label, err)
		}
	}
}

// assertUnsubscribeDown is the "after Down 0060" half: the column and its CHECK are gone, and nothing that is not 0060's went with them.
func assertUnsubscribeDown(t *testing.T, c *pgx.Conn) {
	t.Helper()
	if columnExists(t, c, "jobs", "unsubscribe_url") {
		t.Fatal("after Down 0060: send_byo.jobs.unsubscribe_url is still present (down did not drop it)")
	}
	if constraintExists(t, c, "jobs", "jobs_unsubscribe_url_check") {
		t.Fatal("after Down 0060: jobs_unsubscribe_url_check is still present (down did not drop it)")
	}
	// the control: the neighbours 0060 must never touch are still there
	for _, col := range []string{"rendered_body", "rendered_text", "cancel_requested_at"} {
		if !columnExists(t, c, "jobs", col) {
			t.Fatalf("after Down 0060: send_byo.jobs.%s is GONE — the down must drop only unsubscribe_url", col)
		}
	}
	if !constraintExists(t, c, "jobs", "jobs_rendered_text_size_check") {
		t.Fatal("after Down 0060: jobs_rendered_text_size_check (0058's) is GONE — the down must drop only its own CHECK")
	}
}

// assertCampaignExpiryReseed is the "after Up" half for 0061: the owner's 30 days, in BOTH value and default_value (Rule 1: a fresh row at a new seed site), the
// type unchanged, the sweep's own dial and the mode dial untouched (the over-delete guard: 0061 deletes one key and re-inserts that one key).
func assertCampaignExpiryReseed(t *testing.T, c *pgx.Conn) {
	t.Helper()
	if v, d := configValueAndDefault(t, c, "campaign_expiry_sec"); v != `{"v": 2592000}` || d != `{"v": 2592000}` {
		t.Fatalf("after Up: campaign_expiry_sec value=%s default_value=%s, want both {\"v\": 2592000} (30 days, the owner's 2026-11-27 decision; send_byo_0061 not applied, or 0042's 604800 survived)", v, d)
	}
	if vt := configValueType(t, c, "campaign_expiry_sec"); vt != "integer" {
		t.Fatalf("after Up: campaign_expiry_sec value_type=%q, want \"integer\" (the adminapi validator switches on it)", vt)
	}
	var desc string
	if err := c.QueryRow(context.Background(), `SELECT description FROM send_byo.config WHERE key='campaign_expiry_sec'`).Scan(&desc); err != nil {
		t.Fatalf("after Up: read campaign_expiry_sec description: %v", err)
	}
	if !strings.Contains(desc, "30 days") || strings.Contains(desc, "(default 7 days)") {
		t.Fatalf("after Up: campaign_expiry_sec's description still says 7 days (a documented default that is not the seeded one): %q", desc)
	}
	// the neighbours: the sweep cadence is the seed of 0042 and the expiry MODE is still shadow (promotion is a dated go-live step, not a migration)
	if v, d := configValueAndDefault(t, c, "campaign_expiry_sweep_interval_ms"); v != `{"v": 60000}` || d != `{"v": 60000}` {
		t.Fatalf("after Up: campaign_expiry_sweep_interval_ms value=%s default_value=%s, want both {\"v\": 60000} (0061 must touch campaign_expiry_sec only)", v, d)
	}
	if v, d := configValueAndDefault(t, c, "campaign_expiry_mode"); v != `{"v": "shadow"}` || d != `{"v": "shadow"}` {
		t.Fatalf("after Up: campaign_expiry_mode value=%s default_value=%s, want both {\"v\": \"shadow\"} (the 30-day window must not promote the sweep)", v, d)
	}
}

// assertCampaignExpiryDown is the "after Down 0061" half: the ORIGINAL 0042 row is back, verbatim (7 days), and the neighbours survive.
func assertCampaignExpiryDown(t *testing.T, c *pgx.Conn) {
	t.Helper()
	if !configKeyExists(t, c, "campaign_expiry_sec") {
		t.Fatal("after Down 0061: send_byo.config has no campaign_expiry_sec (the down deleted it and did not restore 0042's row)")
	}
	if v, d := configValueAndDefault(t, c, "campaign_expiry_sec"); v != `{"v": 604800}` || d != `{"v": 604800}` {
		t.Fatalf("after Down 0061: campaign_expiry_sec value=%s default_value=%s, want both {\"v\": 604800} (0042's 7 days restored verbatim)", v, d)
	}
	var desc string
	if err := c.QueryRow(context.Background(), `SELECT description FROM send_byo.config WHERE key='campaign_expiry_sec'`).Scan(&desc); err != nil {
		t.Fatalf("after Down 0061: read the description: %v", err)
	}
	if !strings.Contains(desc, "(default 7 days)") {
		t.Fatalf("after Down 0061: the restored description is not 0042's (it must say \"(default 7 days)\"): %q", desc)
	}
	for _, key := range []string{"campaign_expiry_sweep_interval_ms", "campaign_expiry_mode"} {
		if !configKeyExists(t, c, key) {
			t.Fatalf("after Down 0061: %q is GONE — the down must touch campaign_expiry_sec only", key)
		}
	}
}

// scratchDSN returns dsn with its database replaced by name (the migrator needs a DSN string, and pgx.ConnConfig cannot hand one back for a changed Database).
func scratchDSN(t *testing.T, dsn, name string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	u.Path = "/" + name
	return u.String()
}

// TestPreflight_RequiresUnsubscribeURL: a public.job_items that has rendered_text but NOT unsubscribe_url (a platform that has not run platform_0017) is refused by
// the pre-flight, the message NAMES the column, and the migrator itself refuses to migrate: Runner.Up returns that error and applies NOTHING (no send_byo schema, no
// schema_migrations). The paired control is the same table with the column added: the SAME script passes, and the SAME Runner.Up now applies the chain through 0060,
// so the refusal cannot be "the script always fails" or "the migrator never runs". Its own scratch database, so the lifecycle database is untouched.
func TestPreflight_RequiresUnsubscribeURL(t *testing.T) {
	dsn := testDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	base, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	sqlBytes, err := os.ReadFile(preflightPathForTest())
	if err != nil {
		t.Fatalf("read preflight: %v", err)
	}
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close(context.Background())
	const scratch = "send_byo_preflight_probe_unsub"
	mustExec(t, admin, "DROP DATABASE IF EXISTS "+scratch)
	mustExec(t, admin, "CREATE DATABASE "+scratch)
	defer func() {
		c, cerr := pgx.Connect(context.Background(), dsn)
		if cerr == nil {
			_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+scratch)
			c.Close(context.Background())
		}
	}()
	cfg := base.Copy()
	cfg.Database = scratch
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect scratch: %v", err)
	}
	defer conn.Close(context.Background())

	// a platform that HAS rendered_text (the earlier required column) and does NOT have unsubscribe_url
	mustExec(t, conn, `CREATE TABLE public.users(id uuid primary key default gen_random_uuid(), email text, is_active boolean default true, created_at timestamptz default now())`)
	mustExec(t, conn, `CREATE TABLE public.job_items(id uuid primary key default gen_random_uuid(), campaign_id uuid, user_id uuid, recipient_email text, rendered_subject text, rendered_body text, rendered_text text, reply_to text, status text, created_at timestamptz default now())`)

	// (1) the script refuses, and says which column
	_, err = conn.Exec(ctx, string(sqlBytes))
	if err == nil {
		t.Fatal("the pre-flight ACCEPTED a public.job_items without unsubscribe_url — a platform that was not migrated first (platform_0017) would make every POST /v1/run answer 500")
	}
	if !strings.Contains(err.Error(), "unsubscribe_url") {
		t.Fatalf("the pre-flight refused for the wrong reason (it must name unsubscribe_url): %v", err)
	}

	// (2) the migrator refuses to migrate: the error carries the column's name, and nothing was applied
	r := &Runner{
		DSN:              scratchDSN(t, dsn, scratch),
		Dir:              migrationsDirForTest(),
		PreflightPath:    preflightPathForTest(),
		LockTimeout:      "10s",
		StatementTimeout: "60s",
	}
	upErr := r.Up(ctx)
	if upErr == nil {
		t.Fatal("Runner.Up migrated a database whose public.job_items has no unsubscribe_url — the pre-flight did not stop it")
	}
	if !strings.Contains(upErr.Error(), "unsubscribe_url") {
		t.Fatalf("Runner.Up refused for the wrong reason (its error must name unsubscribe_url): %v", upErr)
	}
	if schemaMigrationsExists(t, conn) {
		t.Fatal("Runner.Up reported the refusal but send_byo.schema_migrations exists: part of the chain was applied before the pre-flight stopped it")
	}

	// (3) the control: add the column, and the SAME script passes and the SAME runner applies the chain, 0060 included
	mustExec(t, conn, `ALTER TABLE public.job_items ADD COLUMN unsubscribe_url text`)
	if _, err := conn.Exec(ctx, string(sqlBytes)); err != nil {
		t.Fatalf("control: the pre-flight REFUSED a public.job_items that has rendered_text and unsubscribe_url: %v", err)
	}
	if err := r.Up(ctx); err != nil {
		t.Fatalf("control: Runner.Up failed on a platform that has both columns: %v", err)
	}
	applied := versions(t, conn)
	if len(applied) == 0 || !containsString(applied, "0060") || !containsString(applied, "0061") {
		t.Fatalf("control: Runner.Up reported success but the chain does not include 0060 and 0061: %v", applied)
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
