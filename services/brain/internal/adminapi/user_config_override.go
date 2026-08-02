package adminapi

// user_config_override.go — Module 3 Phase 6 §4: the admin-only per-user config override write
// path. Two writers (§4.2, #2/#3): PUT sets one or more of the six overridable dials, DELETE clears
// one dial back to inherit (NULL). Both pass through writeUserConfigOverride (§4.3), the ONE
// store-layer function that writes byosmtp.user_config_overrides and refuses a write carrying no
// admin actor identity — "not the handler — the store function," same class, same remedy as Phase
// 4 §2.2/M3-P4-OD-9's From-gate boundary.
//
// §4.3: "there is no legitimate break-glass variant here, so none is provided." Unlike the From
// gate's override flag, an admin override write is ALREADY the privileged path — there is no
// second, more-privileged tier to break glass INTO.

import (
	"context"
	"errors"
	"net/http"

	"github.com/byosmtp-module/brain/internal/configordering"
	"github.com/byosmtp-module/brain/internal/userconfig"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// adminActorID is the sentinel admin actor identity writeUserConfigOverride requires (§4.3). V1's
// Authenticator (auth.go) has exactly one scope and no per-principal DB table — "any valid key
// grants it" — so there is no real per-operator UUID to pass. The requirement §4.3 states
// ("refuses a write that does not carry an admin actor identity... not satisfiable by passing a
// user's own id") is satisfied by a FIXED, non-user-representable sentinel, mirroring server.go's
// own SystemUserID pattern: a caller cannot forge it by supplying any real user_id
// (writeUserConfigOverride's own check below refuses actorID == the target userID), and every
// route that reaches the store function supplies it — so the only way to write this table with NO
// identity at all is a direct DB write outside the module (Rule 1's own accepted runner exception).
const adminActorID = "00000000-0000-0000-0000-00000000adf1"

type userConfigOverrideReq struct {
	SendDelayMinSec    *int `json:"send_delay_min_sec"`
	SendDelayMaxSec    *int `json:"send_delay_max_sec"`
	ConcurrencyPerUser *int `json:"concurrency_per_user"`
	MaxCampaignSize    *int `json:"max_campaign_size"`
	MaxQueuedJobs      *int `json:"max_queued_jobs"`
	CampaignExpirySec  *int `json:"campaign_expiry_sec"`
}

// handleUserConfigOverridePut: PUT /v1/admin/users/{id}/config (§4.2 writer #2). Present fields set
// that dial's override; ABSENT fields are left untouched (a partial PUT does not clear the others —
// use DELETE .../config/{dial} to revert one dial to inherit). At least one field must be present.
func (s *Server) handleUserConfigOverridePut(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	if !isUUID(userID) {
		s.fail(w, r, newErr(codeNotFound, "user_id_malformed"))
		return
	}
	var req userConfigOverrideReq
	present, derr := decodeStrict(r, &req)
	if derr != nil {
		s.fail(w, r, derr)
		return
	}
	if len(present) == 0 {
		s.fail(w, r, newErr(codeValidationFailed, "no_fields_present").
			withDetails(fieldDetail{Field: "body", Issue: issueRequired}))
		return
	}

	ctx, cancel := dbCtx(r.Context())
	defer cancel()

	exists, uerr := userExists(ctx, s.db, userID)
	if uerr != nil {
		s.fail(w, r, newErr(codeInternalError, "user_lookup_error: "+uerr.Error()))
		return
	}
	if !exists {
		s.fail(w, r, newErr(codeNotFound, "user_not_found:"+userID))
		return
	}

	// Structural domain check (mirrors user_config_overrides_positive_check) BEFORE the write's own
	// transaction — a value the CHECK constraint would reject anyway gets a clean apiError naming
	// the field, not a raw 23514.
	type domainField struct {
		field  string
		val    *int
		zeroOK bool // send_delay_{min,max}_sec permit 0; concurrency/caps/expiry must be strictly positive
	}
	for _, f := range []domainField{
		{"send_delay_min_sec", req.SendDelayMinSec, true},
		{"send_delay_max_sec", req.SendDelayMaxSec, true},
		{"concurrency_per_user", req.ConcurrencyPerUser, false},
		{"max_campaign_size", req.MaxCampaignSize, false},
		{"max_queued_jobs", req.MaxQueuedJobs, false},
		{"campaign_expiry_sec", req.CampaignExpirySec, false},
	} {
		if f.val == nil {
			continue
		}
		if *f.val < 0 || (!f.zeroOK && *f.val == 0) {
			s.fail(w, r, newErr(codeValidationFailed, "value_out_of_range:"+f.field).
				withDetails(fieldDetail{Field: f.field, Issue: issueOutOfRange}))
			return
		}
	}

	verr := writeUserConfigOverride(ctx, s.db, userID, adminActorID, req)
	if verr != nil {
		s.fail(w, r, verr)
		return
	}

	row, err := readUserConfigOverride(ctx, s.db, userID)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "override_reread_error: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, row)
}

// handleUserConfigOverrideGet: GET /v1/admin/users/{id}/config — the admin read-back (§1.4's
// non-hot consumer). Absent row (never overridden) still returns 200 with every dial null, not 404
// — "no override" is a valid, common state, not an error.
func (s *Server) handleUserConfigOverrideGet(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	if !isUUID(userID) {
		s.fail(w, r, newErr(codeNotFound, "user_id_malformed"))
		return
	}
	ctx, cancel := dbCtx(r.Context())
	defer cancel()
	exists, uerr := userExists(ctx, s.db, userID)
	if uerr != nil {
		s.fail(w, r, newErr(codeInternalError, "user_lookup_error: "+uerr.Error()))
		return
	}
	if !exists {
		s.fail(w, r, newErr(codeNotFound, "user_not_found:"+userID))
		return
	}
	row, err := readUserConfigOverride(ctx, s.db, userID)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "override_read_error: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, row)
}

// dialColumn maps the §4.2 writer #3 path parameter to its column name — a closed set, never
// interpolated from unchecked input (the path segment is validated against this map's keys before
// use).
var dialColumn = map[string]string{
	"send_delay_min_sec":   "send_delay_min_sec",
	"send_delay_max_sec":   "send_delay_max_sec",
	"concurrency_per_user": "concurrency_per_user",
	"max_campaign_size":    "max_campaign_size",
	"max_queued_jobs":      "max_queued_jobs",
	"campaign_expiry_sec":  "campaign_expiry_sec",
}

// handleUserConfigOverrideDelete: DELETE /v1/admin/users/{id}/config/{dial} (§4.2 writer #3) —
// clear one override column back to NULL (inherit). An unknown dial name is a clean 404, not a
// silent no-op on an unmapped column.
func (s *Server) handleUserConfigOverrideDelete(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	dial := r.PathValue("dial")
	if !isUUID(userID) {
		s.fail(w, r, newErr(codeNotFound, "user_id_malformed"))
		return
	}
	col, ok := dialColumn[dial]
	if !ok {
		s.fail(w, r, newErr(codeNotFound, "dial_not_found:"+dial))
		return
	}
	ctx, cancel := dbCtx(r.Context())
	defer cancel()
	exists, uerr := userExists(ctx, s.db, userID)
	if uerr != nil {
		s.fail(w, r, newErr(codeInternalError, "user_lookup_error: "+uerr.Error()))
		return
	}
	if !exists {
		s.fail(w, r, newErr(codeNotFound, "user_not_found:"+userID))
		return
	}
	if verr := clearUserConfigOverrideDial(ctx, s.db, userID, adminActorID, col); verr != nil {
		s.fail(w, r, verr)
		return
	}
	row, err := readUserConfigOverride(ctx, s.db, userID)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "override_reread_error: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, row)
}

// userConfigOverrideRow is the admin read-back / write-response shape (§1.4 admin read-back, §4.2).
type userConfigOverrideRow struct {
	UserID             string  `json:"user_id"`
	SendDelayMinSec    *int    `json:"send_delay_min_sec"`
	SendDelayMaxSec    *int    `json:"send_delay_max_sec"`
	ConcurrencyPerUser *int    `json:"concurrency_per_user"`
	MaxCampaignSize    *int    `json:"max_campaign_size"`
	MaxQueuedJobs      *int    `json:"max_queued_jobs"`
	CampaignExpirySec  *int    `json:"campaign_expiry_sec"`
	UpdatedBy          *string `json:"updated_by"`
}

func readUserConfigOverride(ctx context.Context, db pgxDB, userID string) (userConfigOverrideRow, error) {
	row := userConfigOverrideRow{UserID: userID}
	err := db.QueryRow(ctx,
		`SELECT send_delay_min_sec, send_delay_max_sec, concurrency_per_user,
		        max_campaign_size, max_queued_jobs, campaign_expiry_sec, updated_by::text
		   FROM byosmtp.user_config_overrides WHERE user_id=$1`, userID).
		Scan(&row.SendDelayMinSec, &row.SendDelayMaxSec, &row.ConcurrencyPerUser,
			&row.MaxCampaignSize, &row.MaxQueuedJobs, &row.CampaignExpirySec, &row.UpdatedBy)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return userConfigOverrideRow{}, err
	}
	return row, nil // no row => every field stays nil (never overridden), not an error
}

// writeUserConfigOverride is §4.3's SINGLE store-layer function that writes
// byosmtp.user_config_overrides. actorID is a REQUIRED argument, not an optional field — a caller
// with no admin identity gets a refusal, and passing the target user's OWN id does not satisfy it
// (a self-service override would defeat the entire admin-only boundary §4.2's structural separation
// exists to enforce). It validates the RESOLVED pacing pair (§4.4 direction 1: submitted override ∪
// current globals) inside the SAME transaction as the write, under the config-write serializer's
// advisory lock (§4.4: "override writes take the SAME lock as global writes... the pair being
// validated spans both tables, and two different locks serialize neither against the other").
func writeUserConfigOverride(ctx context.Context, db *pgxpool.Pool, userID, actorID string, req userConfigOverrideReq) *apiError {
	if actorID == "" || actorID == userID {
		return newErr(codeInternalError, "admin_actor_identity_required")
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		return newErr(codeInternalError, "override_tx_begin_error: "+err.Error())
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", configWriteLockKey); err != nil {
		return newErr(codeInternalError, "override_write_lock_error: "+err.Error())
	}

	// §4.4 direction 1: validate the RESOLVED pacing pair (submitted override ∪ current row ∪
	// current globals) before the write lands.
	if operr := checkResolvedDelayOrderOnOverrideWrite(ctx, tx, userID, req); operr != nil {
		return operr
	}

	_, err = tx.Exec(ctx, `
INSERT INTO byosmtp.user_config_overrides
    (user_id, send_delay_min_sec, send_delay_max_sec, concurrency_per_user,
     max_campaign_size, max_queued_jobs, campaign_expiry_sec, updated_by, updated_at)
VALUES ($1, $2::int, $3::int, $4::int, $5::int, $6::int, $7::int, $8::uuid, NOW())
ON CONFLICT (user_id) DO UPDATE SET
    send_delay_min_sec   = COALESCE($2::int, byosmtp.user_config_overrides.send_delay_min_sec),
    send_delay_max_sec   = COALESCE($3::int, byosmtp.user_config_overrides.send_delay_max_sec),
    concurrency_per_user = COALESCE($4::int, byosmtp.user_config_overrides.concurrency_per_user),
    max_campaign_size    = COALESCE($5::int, byosmtp.user_config_overrides.max_campaign_size),
    max_queued_jobs      = COALESCE($6::int, byosmtp.user_config_overrides.max_queued_jobs),
    campaign_expiry_sec  = COALESCE($7::int, byosmtp.user_config_overrides.campaign_expiry_sec),
    updated_by = $8::uuid,
    updated_at = NOW()`,
		userID, req.SendDelayMinSec, req.SendDelayMaxSec, req.ConcurrencyPerUser,
		req.MaxCampaignSize, req.MaxQueuedJobs, req.CampaignExpirySec, actorID)
	if err != nil {
		if pgErrCode(err) == sqlstateCheckViolation {
			return newErr(codeValidationFailed, "override_check_violation")
		}
		return newErr(codeInternalError, "override_write_error: "+err.Error())
	}
	if err := tx.Commit(ctx); err != nil {
		return newErr(codeInternalError, "override_commit_error: "+err.Error())
	}
	return nil
}

// clearUserConfigOverrideDial clears ONE column back to NULL (inherit) — writer #3 (§4.2), through
// the SAME store-layer actor-identity discipline as writeUserConfigOverride. Clearing a dial can
// only ever WIDEN the resolved pacing pair back toward the (already-valid) global default, never
// invert it, so no §4.4 check is needed on this path — the invariant is checked at every WRITE that
// could invert it, and NULLing a column is not one.
func clearUserConfigOverrideDial(ctx context.Context, db pgxDB, userID, actorID, column string) *apiError {
	if actorID == "" || actorID == userID {
		return newErr(codeInternalError, "admin_actor_identity_required")
	}
	// column comes only from dialColumn's closed map (validated by the caller), never raw path
	// input — safe to interpolate into the SET list.
	if _, err := db.Exec(ctx,
		`UPDATE byosmtp.user_config_overrides SET `+column+` = NULL, updated_by = $2::uuid, updated_at = NOW() WHERE user_id = $1`,
		userID, actorID); err != nil {
		return newErr(codeInternalError, "override_clear_error: "+err.Error())
	}
	return nil
}

// checkResolvedDelayOrderOnOverrideWrite is §4.4 direction 1: an override write must validate the
// RESOLVED pair (submitted override ∪ current row ∪ current globals), inside the write's own
// transaction, under the config-write serializer's lock — never a fresh, out-of-transaction
// counterpart read (the exact defect Phase 10's R4 gate found for the global-only case, reused here
// because the pair being validated now spans two tables instead of one column pair).
//
// Delegates the resolution itself to internal/userconfig.ResolveDelayPair — the SAME function
// checkGlobalDelayWriteAgainstAllOverrides (config_ordering_write.go, §4.4 direction 2) calls, so
// the two directions of this one invariant can never silently disagree on what "resolved" means.
func checkResolvedDelayOrderOnOverrideWrite(ctx context.Context, tx pgxDB, userID string, req userConfigOverrideReq) *apiError {
	// Only relevant when this write touches at least one of the pacing-delay columns — a write to,
	// say, max_queued_jobs alone cannot invert a pacing pair it does not touch.
	if req.SendDelayMinSec == nil && req.SendDelayMaxSec == nil {
		return nil
	}
	globalMin, err := readConfigIntDirect(ctx, tx, "send_delay_min_sec")
	if err != nil {
		return newErr(codeInternalError, "send_delay_min_sec_lookup_error: "+err.Error())
	}
	globalMax, err := readConfigIntDirect(ctx, tx, "send_delay_max_sec")
	if err != nil {
		return newErr(codeInternalError, "send_delay_max_sec_lookup_error: "+err.Error())
	}
	pair, err := userconfig.ResolveDelayPair(ctx, tx, userID, req.SendDelayMinSec, req.SendDelayMaxSec,
		userconfig.Globals{SendDelayMinSec: globalMin, SendDelayMaxSec: globalMax})
	if err != nil {
		return newErr(codeInternalError, "override_read_error: "+err.Error())
	}
	if v := configordering.CheckDelayOrder(pair.Min, pair.Max); v != nil {
		return newErr(codeConfigOrderingViolation, "config_ordering_violation: "+v.Detail()).
			withMessage("This override would make send_delay_min_sec greater than the resolved send_delay_max_sec.").
			withDetails(fieldDetail{Field: "send_delay_min_sec", Issue: issueConflict})
	}
	return nil
}
