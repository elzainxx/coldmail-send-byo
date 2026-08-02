package adminapi

// user_config_override_test.go — Module 3 Phase 6 §7/§12, T6/T9/T11. Real Postgres, real HTTP
// against the actual production Server (httptest) — no mocks (CLAUDE.md Rule 3).

import (
	"context"
	"testing"
)

// TestOverride_T6cd_AdminRouteRefusesWithoutOperatorKeyOrActorIdentity covers §7 T6's admin-side
// attempts — (c) the admin override route with no key at all, (d) the correct shape but no admin
// actor identity (§4.3's store-layer refusal, exercised directly against the store function so
// this proves the STORE layer itself refuses, not merely that the handler happens to behave) —
// plus the admin-CAN positive control (without which T6 would pass against a route broken for
// everyone). T6's user-surface attempts ((a)/(b)/(e), against /v1/byosmtp/settings) live in
// platformapi's own test suite (user_settings_test.go), since that route is a different package's
// Server this suite does not construct.
func TestOverride_T6cd_AdminRouteRefusesWithoutOperatorKeyOrActorIdentity(t *testing.T) {
	e := newTestEnv(t, "t6nowrite")
	userA := e.seedUser(t, "t6usera@example.com")

	before := e.readOverrideDirect(t, userA)
	if before.concurrency != nil {
		t.Fatal("test setup broken: userA already has a concurrency override")
	}

	// (c) The admin override route with NO admin key at all (simulating "a user session" — this
	// surface has no user-session concept, so the strongest available proxy is unauthenticated).
	rC := e.doRaw(t, "PUT", "/v1/admin/users/"+userA+"/config", map[string]any{"concurrency_per_user": 999}, "")
	if rC.status != 401 {
		t.Fatalf("(c) admin override route without the operator key: status=%d, want 401", rC.status)
	}

	// (d) The admin route with the correct SHAPE but no admin actor identity — §4.3's store-layer
	// refusal, exercised directly against the store function.
	verr := writeUserConfigOverride(context.Background(), e.pool, userA, "", userConfigOverrideReq{ConcurrencyPerUser: intPtr(999)})
	if verr == nil {
		t.Fatal("(d) writeUserConfigOverride with an EMPTY actor id succeeded — the store-layer boundary is not enforced")
	}
	verr2 := writeUserConfigOverride(context.Background(), e.pool, userA, userA, userConfigOverrideReq{ConcurrencyPerUser: intPtr(999)})
	if verr2 == nil {
		t.Fatal("(d) writeUserConfigOverride with actorID == the TARGET user's own id succeeded — self-service override must be refused")
	}

	after := e.readOverrideDirect(t, userA)
	if after.concurrency != nil {
		t.Fatalf("override row changed after the refused attempts: concurrency=%v, want nil (unchanged)", after.concurrency)
	}

	// Control: the SAME write, with the operator key, succeeds and the row DOES change.
	ok := e.do(t, "PUT", "/v1/admin/users/"+userA+"/config", map[string]any{"concurrency_per_user": 7}).requireStatus(t, 200)
	if v, isNum := ok.body["concurrency_per_user"].(float64); !isNum || int(v) != 7 {
		t.Fatalf("admin write: concurrency_per_user=%v, want 7", ok.body["concurrency_per_user"])
	}
	afterAdmin := e.readOverrideDirect(t, userA)
	if afterAdmin.concurrency == nil || *afterAdmin.concurrency != 7 {
		t.Fatalf("row after admin write: concurrency=%v, want 7", afterAdmin.concurrency)
	}
}

// TestOverride_T9a_OverrideWriteInvertingResolvedPairIsRefused — §4.4 direction 1: an override of
// send_delay_min_sec=30 with the (seeded default) global max=10 is refused, external code
// config_ordering_violation, and writes no row. Control: the same write with a non-inverting value
// succeeds.
func TestOverride_T9a_OverrideWriteInvertingResolvedPairIsRefused(t *testing.T) {
	e := newTestEnv(t, "t9aoverride")
	userID := e.seedUser(t, "t9a@example.com")
	e.setGlobalInt(t, "send_delay_min_sec", 0)
	e.setGlobalInt(t, "send_delay_max_sec", 10)

	bad := e.do(t, "PUT", "/v1/admin/users/"+userID+"/config", map[string]any{"send_delay_min_sec": 30})
	bad.requireStatus(t, 400)
	if bad.errorCode() != codeConfigOrderingViolation {
		t.Fatalf("inverting override write: code=%s, want config_ordering_violation: %s", bad.errorCode(), bad.raw)
	}
	row := e.readOverrideDirect(t, userID)
	if row.sendMin != nil {
		t.Fatalf("send_delay_min_sec written despite the refused inverting write: %v", row.sendMin)
	}

	// Control: a non-inverting value succeeds.
	ok := e.do(t, "PUT", "/v1/admin/users/"+userID+"/config", map[string]any{"send_delay_min_sec": 5}).requireStatus(t, 200)
	if v, isNum := ok.body["send_delay_min_sec"].(float64); !isNum || int(v) != 5 {
		t.Fatalf("non-inverting override write: send_delay_min_sec=%v, want 5", ok.body["send_delay_min_sec"])
	}
}

// TestOverride_T9b_GlobalWriteInvertingAnExistingOverrideIsRefused — §4.4 direction 2, "the
// likely-missing direction": with an existing override min=5, a global PUT lowering max to 3 is
// refused, and the global is left UNCHANGED. Control: a non-inverting global lowering succeeds.
func TestOverride_T9b_GlobalWriteInvertingAnExistingOverrideIsRefused(t *testing.T) {
	e := newTestEnv(t, "t9bglobal")
	userID := e.seedUser(t, "t9b@example.com")
	e.setGlobalInt(t, "send_delay_min_sec", 0)
	e.setGlobalInt(t, "send_delay_max_sec", 20)
	e.do(t, "PUT", "/v1/admin/users/"+userID+"/config", map[string]any{"send_delay_min_sec": 5}).requireStatus(t, 200)

	bad := e.do(t, "PUT", "/v1/admin/config/send_delay_max_sec", map[string]any{"value": 3})
	bad.requireStatus(t, 400)
	if bad.errorCode() != codeConfigOrderingViolation {
		t.Fatalf("global write inverting an existing override: code=%s, want config_ordering_violation: %s", bad.errorCode(), bad.raw)
	}
	got := e.do(t, "GET", "/v1/admin/config/send_delay_max_sec", nil).requireStatus(t, 200)
	if v, isNum := got.body["value"].(float64); !isNum || int(v) != 20 {
		t.Fatalf("global send_delay_max_sec after the REFUSED write = %v, want unchanged 20", got.body["value"])
	}

	// Control: a non-inverting global lowering (still >= the override's min=5) succeeds.
	ok := e.do(t, "PUT", "/v1/admin/config/send_delay_max_sec", map[string]any{"value": 10}).requireStatus(t, 200)
	if v, isNum := ok.body["value"].(float64); !isNum || int(v) != 10 {
		t.Fatalf("non-inverting global write: value=%v, want 10", ok.body["value"])
	}
}

// TestOverride_T11_DeletedUsersOverridesCascade — §4.2 writer #5: deleting the public.users row
// removes the override row (ON DELETE CASCADE, no orphan). Control: a DIFFERENT user's override
// survives — proving the cascade is scoped to the FK, not a table-wide side effect.
func TestOverride_T11_DeletedUsersOverridesCascade(t *testing.T) {
	e := newTestEnv(t, "t11cascade")
	userA := e.seedUser(t, "t11a@example.com")
	userB := e.seedUser(t, "t11b@example.com")
	e.do(t, "PUT", "/v1/admin/users/"+userA+"/config", map[string]any{"concurrency_per_user": 4}).requireStatus(t, 200)
	e.do(t, "PUT", "/v1/admin/users/"+userB+"/config", map[string]any{"concurrency_per_user": 9}).requireStatus(t, 200)

	if _, err := e.admin.Exec(context.Background(), `DELETE FROM public.users WHERE id=$1`, userA); err != nil {
		t.Fatalf("delete userA: %v", err)
	}

	rowA := e.readOverrideDirect(t, userA)
	if rowA.concurrency != nil {
		t.Fatalf("userA's override row survived the user delete: concurrency=%v, want no row (cascade)", rowA.concurrency)
	}
	rowB := e.readOverrideDirect(t, userB)
	if rowB.concurrency == nil || *rowB.concurrency != 9 {
		t.Fatalf("userB's override was affected by userA's delete: concurrency=%v, want 9 (unaffected)", rowB.concurrency)
	}
}

type overrideRowDirect struct {
	sendMin, concurrency *int
}

func (e *testEnv) readOverrideDirect(t *testing.T, userID string) overrideRowDirect {
	t.Helper()
	var row overrideRowDirect
	err := e.admin.QueryRow(context.Background(),
		`SELECT send_delay_min_sec, concurrency_per_user FROM byosmtp.user_config_overrides WHERE user_id=$1`, userID).
		Scan(&row.sendMin, &row.concurrency)
	if err != nil && !errIsNoRowsAdmin(err) {
		t.Fatalf("read override row: %v", err)
	}
	return row
}

func (e *testEnv) setGlobalInt(t *testing.T, key string, v int) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE byosmtp.config SET value = jsonb_build_object('v', $2::int) WHERE key=$1`, key, v); err != nil {
		t.Fatalf("set global %s: %v", key, err)
	}
}

func intPtr(n int) *int { return &n }

func errIsNoRowsAdmin(err error) bool {
	return err != nil && err.Error() == "no rows in result set"
}
