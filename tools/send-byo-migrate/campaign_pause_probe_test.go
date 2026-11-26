package main

// campaign_pause_probe_test.go — the lifecycle test's helpers for send_byo_0059 (the two tables of POST /v1/pause and
// POST /v1/resume, Phase 4 C1): send_byo.campaign_pauses (the state) and send_byo.campaign_events (the append-only audit).
// Real Postgres, never a mock (CLAUDE.md Rule 3). Like 0058's probe it DRIVES each property (a refused duplicate, a refused
// action, a refused UPDATE, a cascade) rather than reading a definition back, which would assert the migration's own SQL
// against itself.

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// assertCampaignPauseMigration is the "after Up" half for 0059.
func assertCampaignPauseMigration(t *testing.T, c *pgx.Conn) {
	t.Helper()
	ctx := context.Background()

	// anti-vacuity for the objects: both tables, owned by the schema owner, with the append-only function behind the audit
	for _, tbl := range []string{"campaign_pauses", "campaign_events"} {
		if !tableExists(t, c, tbl) {
			t.Fatalf("after Up: send_byo.%s missing (send_byo_0059 not applied)", tbl)
		}
		assertTableOwner(t, c, "send_byo", tbl, "send_byo_owner")
	}
	if !functionExists(t, c, "fn_campaign_events_reject_update") {
		t.Fatal("after Up: send_byo.fn_campaign_events_reject_update missing (0059 did not create the append-only guard)")
	}

	// ADR-0008's trap: a new table needs EXPLICIT grants. The state table is a normal mutable table; the audit is append-only.
	for _, priv := range []string{"SELECT", "INSERT", "UPDATE", "DELETE"} {
		if !hasTablePrivilege(t, c, "send_byo_app", "send_byo.campaign_pauses", priv) {
			t.Fatalf("after Up: send_byo_app lacks %s on send_byo.campaign_pauses (0059 forgot an explicit grant)", priv)
		}
	}
	for priv, want := range map[string]bool{"SELECT": true, "INSERT": true, "UPDATE": false, "DELETE": false} {
		if got := hasTablePrivilege(t, c, "send_byo_app", "send_byo.campaign_events", priv); got != want {
			t.Fatalf("after Up: send_byo_app %s on send_byo.campaign_events = %v, want %v (the audit is INSERT+SELECT only)", priv, got, want)
		}
	}

	// Drive the behaviour in ONE transaction that is always rolled back, so the lifecycle database is unchanged.
	tx, err := c.Begin(ctx)
	if err != nil {
		t.Fatalf("begin campaign pause probe: %v", err)
	}
	defer tx.Rollback(ctx)

	var userID, otherUser string
	if err := tx.QueryRow(ctx, `INSERT INTO public.users (email, is_active) VALUES ('campaign-pause-probe@sender.local', true) RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("campaign pause probe: seed user: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO public.users (email, is_active) VALUES ('campaign-pause-probe-2@sender.local', true) RETURNING id`).Scan(&otherUser); err != nil {
		t.Fatalf("campaign pause probe: seed second user: %v", err)
	}
	const campaign = "0059c0de-0000-4000-8000-000000000001"

	// guarded runs one statement under a savepoint and returns its error, leaving the transaction usable either way
	guarded := func(sql string, args ...any) error {
		if _, err := tx.Exec(ctx, `SAVEPOINT probe`); err != nil {
			t.Fatalf("savepoint: %v", err)
		}
		_, err := tx.Exec(ctx, sql, args...)
		if err != nil {
			_, _ = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT probe`)
			return err
		}
		_, _ = tx.Exec(ctx, `RELEASE SAVEPOINT probe`)
		return nil
	}

	// the state: one row per (campaign, tenant) — a duplicate is refused naming the key, a second TENANT's row for the same
	// campaign id is accepted (the key is the pair), and a user that does not exist is refused by the foreign key
	if err := guarded(`INSERT INTO send_byo.campaign_pauses (campaign_id, user_id) VALUES ($1, $2)`, campaign, userID); err != nil {
		t.Fatalf("after Up: a pause row was refused: %v", err)
	}
	if err := guarded(`INSERT INTO send_byo.campaign_pauses (campaign_id, user_id) VALUES ($1, $2)`, campaign, userID); err == nil || !strings.Contains(err.Error(), "campaign_pauses_pkey") {
		t.Fatalf("after Up: a DUPLICATE (campaign, user) pause row was not refused by campaign_pauses_pkey: %v", err)
	}
	if err := guarded(`INSERT INTO send_byo.campaign_pauses (campaign_id, user_id) VALUES ($1, $2)`, campaign, otherUser); err != nil {
		t.Fatalf("after Up: the SAME campaign id for a second tenant was refused (the key must be the pair): %v", err)
	}
	if err := guarded(`INSERT INTO send_byo.campaign_pauses (campaign_id, user_id) VALUES ($1, '0059c0de-0000-4000-8000-0000000000ff')`, campaign); err == nil || !strings.Contains(err.Error(), "campaign_pauses_user_fk") {
		t.Fatalf("after Up: a pause row for a user that does not exist was not refused by campaign_pauses_user_fk: %v", err)
	}

	// the audit: both actions are accepted (the control), anything else is refused by the CHECK, the actor is the EXISTING enum
	for _, action := range []string{"paused", "resumed"} {
		if err := guarded(`INSERT INTO send_byo.campaign_events (campaign_id, user_id, action, actor, request_id) VALUES ($1, $2, $3, 'system', 'req_probe')`,
			campaign, userID, action); err != nil {
			t.Fatalf("after Up: an audit row with action %q was refused: %v", action, err)
		}
	}
	if err := guarded(`INSERT INTO send_byo.campaign_events (campaign_id, user_id, action, actor) VALUES ($1, $2, 'cancelled', 'system')`, campaign, userID); err == nil || !strings.Contains(err.Error(), "campaign_events_action_check") {
		t.Fatalf("after Up: an audit row with an action outside {paused, resumed} was not refused by campaign_events_action_check: %v", err)
	}
	var evID string
	if err := tx.QueryRow(ctx, `SELECT id FROM send_byo.campaign_events WHERE campaign_id = $1 AND action = 'paused'`, campaign).Scan(&evID); err != nil {
		t.Fatalf("campaign pause probe: read the audit row: %v", err)
	}
	// append-only: even the superuser's UPDATE is stopped by the trigger (the REVOKE covers the module's own role)
	if err := guarded(`UPDATE send_byo.campaign_events SET action = 'resumed' WHERE id = $1`, evID); err == nil || !strings.Contains(err.Error(), "append-only: UPDATE forbidden") {
		t.Fatalf("after Up: an UPDATE of a campaign_events row was not rejected by the append-only trigger: %v", err)
	}

	// a user delete cascades into both tables (the FK's own DELETE, which no trigger blocks)
	if _, err := tx.Exec(ctx, `DELETE FROM public.users WHERE id = $1`, userID); err != nil {
		t.Fatalf("campaign pause probe: delete the user: %v", err)
	}
	var pauses, events int
	if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM send_byo.campaign_pauses WHERE user_id = $1),
	                                   (SELECT count(*) FROM send_byo.campaign_events WHERE user_id = $1)`, userID).Scan(&pauses, &events); err != nil {
		t.Fatalf("campaign pause probe: count after cascade: %v", err)
	}
	if pauses != 0 || events != 0 {
		t.Fatalf("after Up: deleting the user left %d pause row(s) and %d audit row(s), want 0/0 (ON DELETE CASCADE)", pauses, events)
	}
	// ... and left the OTHER tenant's row alone (the control: the cascade is scoped to the deleted user)
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM send_byo.campaign_pauses WHERE user_id = $1`, otherUser).Scan(&pauses); err != nil || pauses != 1 {
		t.Fatalf("after Up: the cascade removed the OTHER tenant's pause row (count %d, err %v)", pauses, err)
	}
}

// assertCampaignPauseDown is the "after Down 0059" half: the two tables and the guard are gone, and nothing that is not 0059's went with them.
func assertCampaignPauseDown(t *testing.T, c *pgx.Conn) {
	t.Helper()
	for _, tbl := range []string{"campaign_pauses", "campaign_events"} {
		if tableExists(t, c, tbl) {
			t.Fatalf("after Down 0059: send_byo.%s is still present (down did not drop it)", tbl)
		}
	}
	if functionExists(t, c, "fn_campaign_events_reject_update") {
		t.Fatal("after Down 0059: send_byo.fn_campaign_events_reject_update is still present (down did not drop it)")
	}
	// the control: the neighbours 0059 must never touch are still there
	for _, tbl := range []string{"campaigns", "account_events", "job_events", "jobs"} {
		if !tableExists(t, c, tbl) {
			t.Fatalf("after Down 0059: send_byo.%s is GONE — the down must drop only 0059's own tables", tbl)
		}
	}
}
