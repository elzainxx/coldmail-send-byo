package platformapi

// account_cap_test.go — Module 3 Phase 3, C1, §5.2's exit criterion (12 of §12): the account cap
// is enforced on create and counts non-deleted rows only. Bulk-add's own per-row cap behavior is
// C4's test (§4.2's mid-batch cap-crossing case cannot be constructed without bulk-add existing).

import (
	"context"
	"testing"
)

// TestAccountCap_EnforcedOnCreate — with account_count_cap_per_user set low, creating up to the
// cap succeeds and the next create is refused with a clean 409 account_cap_reached. Control: a
// DIFFERENT user is unaffected by the first user's cap (the dial is per-user, not global-total).
func TestAccountCap_EnforcedOnCreate(t *testing.T) {
	e := newTestEnv(t, "acctcap")
	e.setConfigInt(t, "account_count_cap_per_user", 2)

	userID := e.seedUser(t, "cap@example.com")
	e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(userID, "smtp.cap1.com", "cap-user-1")).requireStatus(t, 201)
	e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(userID, "smtp.cap2.com", "cap-user-2")).requireStatus(t, 201)

	over := e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(userID, "smtp.cap3.com", "cap-user-3")).
		requireStatus(t, 409)
	if over.errorCode() != codeConflict {
		t.Fatalf("over-cap create: code=%s, want conflict", over.errorCode())
	}
	if n := e.accountCountForUserDirect(t, userID); n != 2 {
		t.Fatalf("account count for user after over-cap attempt = %d, want 2 (rejected row not inserted)", n)
	}

	// Control: a different user has their OWN cap headroom, unaffected by the first user's.
	otherUser := e.seedUser(t, "capother@example.com")
	e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(otherUser, "smtp.capother.com", "cap-other-user")).
		requireStatus(t, 201)
}

func (e *testEnv) setConfigInt(t *testing.T, key string, v int) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE byosmtp.config SET value = jsonb_build_object('v', $2::int) WHERE key=$1`, key, v); err != nil {
		t.Fatalf("set config %s: %v", key, err)
	}
}

func (e *testEnv) accountCountForUserDirect(t *testing.T, userID string) int {
	t.Helper()
	var n int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM byosmtp.accounts WHERE user_id=$1 AND deleted_at IS NULL`, userID).Scan(&n); err != nil {
		t.Fatalf("count accounts: %v", err)
	}
	return n
}
