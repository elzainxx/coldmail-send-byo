package platformapi

// account_delete_test.go — Module 3 Phase 3, C5, §8.6/§8.8/§8.9 (T6/T8/T9's third control). Real
// Postgres, real HTTP against the actual production Server (no mocks, CLAUDE.md Rule 3).

import (
	"context"
	"testing"
)

// TestAccountDelete_SoftDeletesStampsRetentionAndBecomesUnusable (§6.1/§6.2) — DELETE returns 204,
// the row PERSISTS (not removed) with deleted_at/deleted_by/purge_after all set, and every other
// verb on this surface now returns 404 for it — the SAME not-found a genuinely nonexistent id
// returns (no disclosure of "exists but deleted" vs "never existed").
func TestAccountDelete_SoftDeletesStampsRetentionAndBecomesUnusable(t *testing.T) {
	e := newTestEnv(t, "delbasic")
	e.setConfigInt(t, "account_retention_window_sec", 2592000) // 30 days, matches the seeded default
	userID := e.seedUser(t, "delbasic@example.com")
	acctID := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "smtp.delbasic.com", "delbasic-user")).
		requireStatus(t, 201).str("id")

	del := e.do(t, "DELETE", "/v1/send-byo/accounts/"+acctID+"?user_id="+userID, nil)
	if del.status != 204 {
		t.Fatalf("delete status=%d, want 204: %s", del.status, del.raw)
	}

	var deletedAtSet, deletedByMatches, purgeAfterSet bool
	if err := e.admin.QueryRow(context.Background(),
		`SELECT deleted_at IS NOT NULL, deleted_by = $2, purge_after IS NOT NULL
		   FROM send_byo.accounts WHERE id=$1`, acctID, userID).
		Scan(&deletedAtSet, &deletedByMatches, &purgeAfterSet); err != nil {
		t.Fatalf("read deletion columns: %v", err)
	}
	if !deletedAtSet || !deletedByMatches || !purgeAfterSet {
		t.Fatalf("deletion columns after DELETE: deleted_at_set=%v deleted_by_matches=%v purge_after_set=%v, want all true",
			deletedAtSet, deletedByMatches, purgeAfterSet)
	}
	// The row PERSISTS — §6.1's whole point (retained, not removed).
	var stillExists bool
	if err := e.admin.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM send_byo.accounts WHERE id=$1)`, acctID).Scan(&stillExists); err != nil {
		t.Fatalf("check row exists: %v", err)
	}
	if !stillExists {
		t.Fatal("the account row was REMOVED by DELETE — §6.1 requires soft delete (retained through the window), not a hard delete")
	}

	// Every other verb now 404s (§6.2's "unusable" applied to the USER-FACING surface).
	e.do(t, "GET", "/v1/send-byo/accounts/"+acctID+"?user_id="+userID, nil).requireStatus(t, 404)
	e.do(t, "PATCH", "/v1/send-byo/accounts/"+acctID+"?user_id="+userID,
		map[string]any{"from_name": "should not apply"}).requireStatus(t, 404)
	e.do(t, "POST", "/v1/send-byo/accounts/"+acctID+"/disable?user_id="+userID, nil).requireStatus(t, 404)

	// A second delete is idempotent-shaped: 404, SAME code as a genuinely nonexistent id (no
	// disclosure of "already deleted" vs "never existed").
	redelete := e.do(t, "DELETE", "/v1/send-byo/accounts/"+acctID+"?user_id="+userID, nil)
	if redelete.status != 404 {
		t.Fatalf("second delete status=%d, want 404", redelete.status)
	}
	ghost := e.do(t, "DELETE", "/v1/send-byo/accounts/11111111-2222-3333-4444-555555555555?user_id="+userID, nil)
	if redelete.errorCode() != ghost.errorCode() {
		t.Fatalf("second-delete 404 code %q differs from nonexistent-id 404 code %q — existence disclosure", redelete.errorCode(), ghost.errorCode())
	}
}

// TestAccountDelete_CrossTenantIsNotFound (§8.8, T8 applied to the new verb) — B cannot delete A's
// account: 404, never 403, and A's row is byte-unchanged (still not deleted). Controls: A deleting
// A's own account succeeds (positive); B is genuinely authenticated (same shared token); the
// cross-tenant 404 is byte-identical to a nonexistent-id 404 (shape, no disclosure).
func TestAccountDelete_CrossTenantIsNotFound(t *testing.T) {
	e := newTestEnv(t, "delxtenant")
	userA := e.seedUser(t, "delxA@example.com")
	userB := e.seedUser(t, "delxB@example.com")
	acctA := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userA, "smtp.delxA.com", "delxA-user")).
		requireStatus(t, 201).str("id")

	// B (valid selector, wrong tenant) attempts to delete A's account.
	xdel := e.do(t, "DELETE", "/v1/send-byo/accounts/"+acctA+"?user_id="+userB, nil)
	if xdel.status != 404 {
		t.Fatalf("cross-tenant delete status=%d, want 404 (never 403)", xdel.status)
	}
	var stillLive bool
	if err := e.admin.QueryRow(context.Background(),
		`SELECT deleted_at IS NULL FROM send_byo.accounts WHERE id=$1`, acctA).Scan(&stillLive); err != nil {
		t.Fatalf("read deleted_at: %v", err)
	}
	if !stillLive {
		t.Fatal("A's account was DELETED by B's cross-tenant attempt — the 404 did a write anyway (worse than a plain 404)")
	}

	// Shape control: cross-tenant 404 identical to a nonexistent id's 404.
	ghost := e.do(t, "DELETE", "/v1/send-byo/accounts/11111111-2222-3333-4444-555555555555?user_id="+userB, nil)
	if xdel.errorCode() != ghost.errorCode() {
		t.Fatalf("cross-tenant 404 code %q differs from nonexistent-id 404 code %q — existence disclosure", xdel.errorCode(), ghost.errorCode())
	}

	// Positive control: A deleting A's own account succeeds.
	ok := e.do(t, "DELETE", "/v1/send-byo/accounts/"+acctA+"?user_id="+userA, nil)
	if ok.status != 204 {
		t.Fatalf("A's own delete status=%d, want 204 (the 404 above was tenancy, not a broken route)", ok.status)
	}
}

// TestAccountUniqueness_SoftDeleteThenReAddSameMailbox (§8.9 T9's third control, promised by
// account_uniqueness_test.go's own header comment — soft delete did not exist at C1, this file is
// where it lands). accounts_user_host_username_unique is PARTIAL on deleted_at IS NULL (§5.1): after
// a soft delete, the SAME user re-adding the SAME (host, username) must succeed — a total index
// would wedge every user who deletes and re-adds a mailbox, the exact user-visible failure Phase 1
// §3.1 partial-indexed against.
func TestAccountUniqueness_SoftDeleteThenReAddSameMailbox(t *testing.T) {
	e := newTestEnv(t, "delreadd")
	userID := e.seedUser(t, "delreadd@example.com")
	const host, user = "smtp.delreadd.com", "readd-mailbox"

	first := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, host, user)).requireStatus(t, 201)
	e.do(t, "DELETE", "/v1/send-byo/accounts/"+first.str("id")+"?user_id="+userID, nil).requireStatus(t, 204)

	// Without the soft delete, this would 409 duplicate_account_for_user.
	second := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, host, user))
	if second.status != 201 {
		t.Fatalf("re-add after soft-delete: status=%d, want 201 (accounts_user_host_username_unique must be partial on deleted_at IS NULL): %s", second.status, second.raw)
	}
	if second.str("id") == first.str("id") {
		t.Fatalf("re-add returned the SAME id as the deleted row — a new row was expected, not a resurrection")
	}

	// Both rows now exist for this (host, username): one deleted, one live.
	var total, liveCount int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(*), count(*) FILTER (WHERE deleted_at IS NULL)
		   FROM send_byo.accounts WHERE user_id=$1 AND smtp_host=$2 AND smtp_username=$3`,
		userID, host, user).Scan(&total, &liveCount); err != nil {
		t.Fatalf("count accounts: %v", err)
	}
	if total != 2 || liveCount != 1 {
		t.Fatalf("accounts for (host,username) after re-add: total=%d live=%d, want 2/1", total, liveCount)
	}
}
