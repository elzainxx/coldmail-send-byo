package platformapi

// account_uniqueness_test.go — Module 3 Phase 3, C1, T9 (§8.9). Real Postgres, real HTTP. Per-user
// uniqueness rejects a duplicate (host, username) for the SAME user with a clean 409, while the
// SAME (host, username) under a DIFFERENT user is accepted — the two-layer shape (app pre-check +
// unique-index backstop) inherited from module 2 §2.7.
//
// Control 2 (the soft-delete re-add case) lives in account_delete_test.go (C5) — soft delete does
// not exist yet at C1, so that control cannot be constructed here. Controls 1 and 3 (the ones this
// chunk's own code can prove) are covered below; §8.9's full three-control set is complete once C5
// lands.

import (
	"context"
	"sync"
	"testing"
)

// TestAccountUniqueness_RejectsDuplicateForSameUser_AllowsAcrossUsers (T9) — U1 creates
// (host,username) -> 201; U1 creates it again -> 409 conflict, exactly one row for that triple.
// Control 1 (the one the brief names): U2 creates the SAME (host,username) -> 201 — two tenants
// legitimately holding the same mailbox is allowed; a global unique index would wrongly reject it.
func TestAccountUniqueness_RejectsDuplicateForSameUser_AllowsAcrossUsers(t *testing.T) {
	e := newTestEnv(t, "acctuniq")
	u1 := e.seedUser(t, "uniqU1@example.com")
	u2 := e.seedUser(t, "uniqU2@example.com")
	const host, user = "smtp.uniq.com", "shared-mailbox"

	first := e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(u1, host, user)).requireStatus(t, 201)
	if first.str("id") == "" {
		t.Fatalf("U1's first create returned no id: %s", first.raw)
	}

	dup := e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(u1, host, user)).requireStatus(t, 409)
	if dup.errorCode() != codeConflict {
		t.Fatalf("U1's duplicate create: code=%s, want conflict", dup.errorCode())
	}
	if n := e.countAccountsFor(t, u1, host, user); n != 1 {
		t.Fatalf("U1 has %d rows for (%s,%s) after the rejected duplicate, want exactly 1", n, host, user)
	}

	// Control 1: U2 creating the SAME (host, username) succeeds — cross-tenant collision is allowed.
	crossUser := e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(u2, host, user)).requireStatus(t, 201)
	if crossUser.str("id") == "" {
		t.Fatalf("U2's create of the same (host,username) failed: %s", crossUser.raw)
	}
	if crossUser.str("id") == first.str("id") {
		t.Fatalf("U2's account got the SAME id as U1's — rows collapsed instead of being independent")
	}
}

// TestAccountUniqueness_ConcurrentCreateBackstop (T9, control 3) — two concurrent creates of the
// SAME (user, host, username) triple through two connections: exactly one 201 and one 409, and the
// 409 is the TRANSLATED envelope (never a raw 23505). Proves the unique-index backstop is real,
// not merely that the app-level pre-check alone happens to be enough.
func TestAccountUniqueness_ConcurrentCreateBackstop(t *testing.T) {
	e := newTestEnv(t, "acctuniqrace")
	userID := e.seedUser(t, "uniqrace@example.com")
	const host, user = "smtp.uniqrace.com", "race-mailbox"

	var wg sync.WaitGroup
	results := make([]resp, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(userID, host, user))
		}(i)
	}
	wg.Wait()

	var n201, n409 int
	for _, r := range results {
		switch r.status {
		case 201:
			n201++
		case 409:
			n409++
			if r.errorCode() != codeConflict {
				t.Fatalf("concurrent create loser: code=%s, want conflict (never a raw error)", r.errorCode())
			}
		default:
			t.Fatalf("concurrent create returned unexpected status %d: %s", r.status, r.raw)
		}
	}
	if n201 != 1 || n409 != 1 {
		t.Fatalf("concurrent create outcomes: %d created, %d conflict, want exactly 1/1", n201, n409)
	}
	if n := e.countAccountsFor(t, userID, host, user); n != 1 {
		t.Fatalf("exactly %d row(s) for the raced (user,host,username) triple, want exactly 1", n)
	}
}

func (e *testEnv) countAccountsFor(t *testing.T, userID, host, username string) int {
	t.Helper()
	var n int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM byosmtp.accounts WHERE user_id=$1 AND smtp_host=$2 AND smtp_username=$3`,
		userID, host, username).Scan(&n); err != nil {
		t.Fatalf("count accounts: %v", err)
	}
	return n
}
