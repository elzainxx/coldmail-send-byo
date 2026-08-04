package adminapi

// config_ordering_write.go — the admin PUT-boundary half of the two config-ordering invariants
// (CLAUDE.md Rule 1; Phase 7 §5.3; Phase 9 §4.6/E4). P9-OD-2 (RULED-AND-CLOSED, Docs/
// open-decisions.md): before this file, only the config-LOAD path (internal/dispatch/config.go,
// read on the Control's own tick cadence) enforced these invariants — a PUT of a value that would
// invert either pair returned a clean 200, the bad value landed in send_byo.config, and the
// violation surfaced only later, as a LATENT tick error far from the change that caused it (the
// exact "false promise of control" defect class the 9.5 consumption sweep already closed twice).
// This closes the gap at the point of the mistake: reject the WRITE, don't let it land.
//
// Both invariant CHECKS are the SAME pure functions internal/dispatch/config.go calls
// (internal/configordering) — reused, not reimplemented, so load-time and write-time can never
// silently diverge on what "valid" means for these two pairs.

import (
	"context"
	"fmt"

	"github.com/coldmail-send-byo/control/internal/configordering"
	"github.com/coldmail-send-byo/control/internal/userconfig"
)

// checkConfigOrderingOnWrite validates the RESULTING pair for a PUT to key with candidate value
// newValue — not the submitted value in isolation. Reads the counterpart key's CURRENT stored
// value fresh (never cached) so a write is validated against real, live state. Returns nil for
// any key with no ordering counterpart (every other config key). On violation, returns a clean
// apiError naming the conflicting counterpart key, its current value, and the required relation —
// so an operator is never left guessing which key to change or by how much (two-vocabulary rule:
// this detail is safe operational information, not an internal reason/credential/SQL detail).
func checkConfigOrderingOnWrite(ctx context.Context, db pgxDB, key string, newValue int) *apiError {
	// counterpartVal/explain are keyed on `key` (the one being WRITTEN) — used for the error
	// message, which must name the OTHER key regardless of which side of the invariant `key`
	// happens to be. configordering.Violation's own Key/CounterpartKey fields are fixed to the
	// invariant's semantic structure (dependent value vs boundary value), NOT to which key this
	// particular write touched, so the message is built from these locals, never from v directly.
	var (
		v              *configordering.Violation
		counterpartKey string
		counterpartVal int
		explain        string
	)
	switch key {
	case "smtp_operation_timeout_sec":
		counterpartKey = "job_max_processing_time_sec"
		lease, err := readConfigIntDirect(ctx, db, counterpartKey)
		if err != nil {
			return counterpartReadErr(counterpartKey, err)
		}
		counterpartVal = lease
		v = configordering.CheckSMTPTimeoutVsLease(newValue, lease)
		explain = "smtp_operation_timeout_sec must stay strictly below job_max_processing_time_sec"
	case "job_max_processing_time_sec":
		counterpartKey = "smtp_operation_timeout_sec"
		timeout, err := readConfigIntDirect(ctx, db, counterpartKey)
		if err != nil {
			return counterpartReadErr(counterpartKey, err)
		}
		counterpartVal = timeout
		v = configordering.CheckSMTPTimeoutVsLease(timeout, newValue)
		explain = "job_max_processing_time_sec must stay strictly above smtp_operation_timeout_sec"
	case "box_outcome_buffer_ttl_sec":
		counterpartKey = "startup_reconnect_window_sec"
		window, err := readConfigIntDirect(ctx, db, counterpartKey)
		if err != nil {
			return counterpartReadErr(counterpartKey, err)
		}
		counterpartVal = window
		v = configordering.CheckBufferTTLVsReconnectWindow(newValue, window)
		explain = "box_outcome_buffer_ttl_sec must stay at or above startup_reconnect_window_sec"
	case "startup_reconnect_window_sec":
		counterpartKey = "box_outcome_buffer_ttl_sec"
		buffer, err := readConfigIntDirect(ctx, db, counterpartKey)
		if err != nil {
			return counterpartReadErr(counterpartKey, err)
		}
		counterpartVal = buffer
		v = configordering.CheckBufferTTLVsReconnectWindow(buffer, newValue)
		explain = "startup_reconnect_window_sec must stay at or below box_outcome_buffer_ttl_sec"
	case "quarantine_min_sample":
		// Module 3 Phase 5 §10.4: a single-key floor (>= 1), no counterpart config key — a zero
		// min-sample would let the failure-rate trigger fire on the first failure at all, silently
		// converting it into a one-strike trigger. Rendered directly (no counterpart read needed).
		v = configordering.CheckQuarantineMinSample(newValue)
		if v == nil {
			return nil
		}
		return newErr(codeConfigOrderingViolation, "config_ordering_violation: "+v.Detail()).
			withMessage("quarantine_min_sample must be at least 1 — a zero value would let the failure-rate trigger fire on the very first failure.").
			withDetails(fieldDetail{Field: "value", Issue: issueConflict})
	case "quarantine_failure_rate_pct":
		// Module 3 Phase 5 §10.4: a single-key range ([1,100]), no counterpart config key.
		v = configordering.CheckQuarantineFailureRatePct(newValue)
		if v == nil {
			return nil
		}
		return newErr(codeConfigOrderingViolation, "config_ordering_violation: "+v.Detail()).
			withMessage("quarantine_failure_rate_pct must be between 1 and 100 inclusive.").
			withDetails(fieldDetail{Field: "value", Issue: issueConflict})
	case "send_delay_min_sec":
		counterpartKey = "send_delay_max_sec"
		max, err := readConfigIntDirect(ctx, db, counterpartKey)
		if err != nil {
			return counterpartReadErr(counterpartKey, err)
		}
		counterpartVal = max
		v = configordering.CheckDelayOrder(newValue, max)
		explain = "send_delay_min_sec must stay at or below send_delay_max_sec"
		// §4.4 direction 2: this global write must ALSO not invert the RESOLVED pair for any user
		// who has overridden ONLY send_delay_max_sec — the row-level CHECK on
		// user_config_overrides cannot see this global at all. Checked here (v is still the
		// global-only verdict above; a nil v does not exempt this check).
		if operr := checkGlobalDelayWriteAgainstAllOverrides(ctx, db, "send_delay_min_sec", newValue); operr != nil {
			return operr
		}
	case "send_delay_max_sec":
		counterpartKey = "send_delay_min_sec"
		min, err := readConfigIntDirect(ctx, db, counterpartKey)
		if err != nil {
			return counterpartReadErr(counterpartKey, err)
		}
		counterpartVal = min
		v = configordering.CheckDelayOrder(min, newValue)
		explain = "send_delay_max_sec must stay at or above send_delay_min_sec"
		if operr := checkGlobalDelayWriteAgainstAllOverrides(ctx, db, "send_delay_max_sec", newValue); operr != nil {
			return operr
		}
	default:
		return nil // no ordering counterpart for this key
	}
	if v == nil {
		return nil
	}
	return newErr(codeConfigOrderingViolation, "config_ordering_violation: "+v.Detail()).
		withMessage(fmt.Sprintf("This value conflicts with %s (currently %d): %s.",
			counterpartKey, counterpartVal, explain)).
		withDetails(fieldDetail{Field: "value", Issue: issueConflict})
}

// counterpartReadErr wraps a failed read of a key's ordering counterpart. Both members of both
// pairs are seeded since send_byo_0010/0018/0019, so a read failure here is an internal fault, not
// a validation question — fail closed (reject the write) rather than silently skip the check.
func counterpartReadErr(counterpartKey string, err error) *apiError {
	return newErr(codeInternalError, "config_ordering_counterpart_read_failed key="+counterpartKey+": "+err.Error())
}

// checkGlobalDelayWriteAgainstAllOverrides is Module 3 Phase 6 §4.4 direction 2 — "a global PUT
// must validate against EVERY existing override row, or lowering the global send_delay_max_sec
// inverts the pair for every user who overrode only the min." A row-level CHECK on
// user_config_overrides cannot see this write at all (it lives in a different table), so this is
// the ONLY enforcement point for this direction. writtenField is "send_delay_min_sec" or
// "send_delay_max_sec"; newValue is the candidate value about to be written to the global.
//
// Delegates the actual override→global resolution to internal/userconfig
// (FindDelayPairInvertedByGlobalWrite) rather than a local SQL COALESCE — §1.1's "no consumer
// applies its own COALESCE" applies to this check exactly as it does to any other reader of
// user_config_overrides; the resolution rule has ONE definition, shared with the override-write
// direction (checkResolvedDelayOrderOnOverrideWrite in user_config_override.go).
func checkGlobalDelayWriteAgainstAllOverrides(ctx context.Context, db pgxDB, writtenField string, newValue int) *apiError {
	globalMin, err := readConfigIntDirect(ctx, db, "send_delay_min_sec")
	if err != nil {
		return newErr(codeInternalError, "send_delay_min_sec_lookup_error: "+err.Error())
	}
	globalMax, err := readConfigIntDirect(ctx, db, "send_delay_max_sec")
	if err != nil {
		return newErr(codeInternalError, "send_delay_max_sec_lookup_error: "+err.Error())
	}
	pair, found, ferr := userconfig.FindDelayPairInvertedByGlobalWrite(ctx, db, writtenField, newValue,
		userconfig.Globals{SendDelayMinSec: globalMin, SendDelayMaxSec: globalMax})
	if ferr != nil {
		return newErr(codeInternalError, "override_resolved_check_error: "+ferr.Error())
	}
	if !found {
		return nil
	}
	otherCounterpartKey, otherResolved := "send_delay_max_sec", pair.Max
	if writtenField == "send_delay_max_sec" {
		otherCounterpartKey, otherResolved = "send_delay_min_sec", pair.Min
	}
	v := &configordering.Violation{
		Key: writtenField, Value: newValue, Constraint: "must not invert the resolved pacing pair for",
		CounterpartKey: "user " + pair.UserID + "'s resolved " + otherCounterpartKey, CounterpartValue: otherResolved,
	}
	return newErr(codeConfigOrderingViolation, "config_ordering_violation: "+v.Detail()).
		withMessage(fmt.Sprintf("Lowering/raising %s would invert the resolved pacing pair for at least one user (id=%s) who has overridden only %s.",
			writtenField, pair.UserID, otherCounterpartKey)).
		withDetails(fieldDetail{Field: "value", Issue: issueConflict})
}

// readConfigIntDirect reads one integer-typed send_byo.config key's CURRENT value, unwrapping the
// {"v": N} envelope. Distinct from getConfig's caller-facing configRow because this needs only
// the raw int, and must read fresh inside the SAME TRANSACTION as the write it is guarding — never
// a cached value, and never on a separate connection (see handleConfigPut on why the transaction,
// not merely the freshness, is what makes the check sound).
func readConfigIntDirect(ctx context.Context, db pgxDB, key string) (int, error) {
	row, err := getConfig(ctx, db, key)
	if err != nil {
		return 0, err
	}
	n, ok := asInt(row.Value)
	if !ok {
		return 0, fmt.Errorf("config %q is not an integer", key)
	}
	return int(n), nil
}
