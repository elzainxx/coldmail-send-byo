// from_gate.go — Module 3 Phase 4 §2/§4.6/§5/§8's STORE-LAYER gate, wired into every writer of
// from_address on this surface (create, PATCH, bulk-add rows — §2.2's routes 1-3). §2.8/M3-P4-OD-9
// requires the gate live at the store layer, not the handler, so a future handler cannot bypass it
// by construction; this file is the single call site every writer routes through.
package platformapi

import (
	"context"
	"encoding/json"
	"time"

	"github.com/coldmail-send-byo/control/internal/fromgate"
)

// fromGateResult is what runFromGate hands back to a caller deciding how to render a response and
// what columns to add to its own INSERT/UPDATE — this package's own narrow view of
// fromgate.Verdict.
type fromGateResult struct {
	verdict fromgate.Verdict

	// acknowledgeRequired is true iff the verdict is WARN and the caller did not supply the
	// acknowledgment field — M3-P4-OD-15: the caller must refuse the WHOLE write (no fallback
	// value exists for from_address, which is NOT NULL) rather than persist anything.
	acknowledgeRequired bool
}

// acknowledgeAlignmentWarningField is this surface's JSON field name for §5.3.1's "explicit
// acknowledgment field on the request" — the spec states the requirement without naming the
// field; named here to match this codebase's descriptive-JSON-key convention (from_address,
// daily_limit, ...). Present + true is the only accepted form; present-but-false or absent both
// count as "not acknowledged" (silence is never consent, §5.3.1).
const acknowledgeAlignmentWarningField = "acknowledge_alignment_warning"

// runFromGate evaluates the From alignment gate for one (fromAddress, smtpHost, smtpUsername)
// triple and classifies the result against acknowledged (whether the caller's request supplied
// the acknowledgment field). It performs NO DB write itself.
func (s *Server) runFromGate(ctx context.Context, fromAddress, smtpHost, smtpUsername string, acknowledged bool) fromGateResult {
	opts := s.fromGateOptions(ctx)
	v := fromgate.Evaluate(ctx, fromAddress, smtpHost, smtpUsername, opts)
	return fromGateResult{
		verdict:             v,
		acknowledgeRequired: v.Outcome == fromgate.OutcomeWarn && !acknowledged,
	}
}

// fromGateOptions resolves §6.1's seven config dials fresh from send_byo.config (config-not-code,
// CLAUDE.md Rule 1 — no redeploy needed for a value change to take effect). Falls back to the
// seeded defaults only on a genuine read/parse failure, mirroring every other config reader on
// this surface (readBulkAddMaxRows, readAccountCountCap, ...) — never a silent "gate disabled" on
// a transient read failure, since from_gate_mode's own "off" already exists as the deliberate,
// operator-chosen escape hatch and a read failure defaulting to enforce is fail-closed, not
// fail-open.
func (s *Server) fromGateOptions(ctx context.Context) fromgate.Options {
	mode, ok, err := s.readConfigString(ctx, "from_gate_mode")
	if err != nil || !ok {
		mode = "enforce"
	}
	resolverAddr, ok, err := s.readConfigString(ctx, "from_dns_resolver")
	if err != nil || !ok {
		resolverAddr = ""
	}
	lookupMS, ok, err := s.readConfigInt(ctx, "from_dns_lookup_timeout_ms")
	if err != nil || !ok {
		lookupMS = 3000
	}
	budgetMS, ok, err := s.readConfigInt(ctx, "from_dns_total_budget_ms")
	if err != nil || !ok {
		budgetMS = 15000
	}
	posture, ok, err := s.readConfigString(ctx, "from_spf_lookup_limit_posture")
	if err != nil || !ok {
		posture = "warn"
	}
	return fromgate.Options{
		DNSResolverAddr:       resolverAddr,
		LookupTimeout:         time.Duration(lookupMS) * time.Millisecond,
		TotalBudget:           time.Duration(budgetMS) * time.Millisecond,
		SPFLookupLimitPosture: posture,
		GateMode:              mode,
		Cache:                 s.fromGateCache,
	}
}

// fromGateColumnSet is one (column, value) pair for a caller's own INSERT/UPDATE builder.
// Val is either a real bind value or nowLiteral, which callers render as the literal SQL NOW()
// rather than binding it as a parameter — from_checked_at must be the DATABASE's clock (the same
// convention every other timestamp column on this surface already uses, e.g. updated_at), not the
// Control process's.
type fromGateColumnSet struct {
	Col string
	Val any
}

// nowLiteral is fromGateColumnSet.Val's sentinel for "the literal SQL NOW()".
type nowLiteral struct{}

// fromGateVerdictSets returns the (column, value) pairs a caller's own INSERT/UPDATE builder
// should add for a DEFINITIVE (non-indeterminate) verdict — M3-P4-OD-14: an indeterminate outcome
// returns a nil slice and writes NOTHING (the row keeps/gets its existing or default from_*
// values, exactly as if the gate had not run this time); the caller's own response separately
// reports the indeterminate outcome via res.verdict directly.
//
// Deliberately EXCLUDES the acknowledgment pair — those are written by fromGateWriteAcknowledgment
// as a SEPARATE, later statement in the SAME transaction/request, because from_verdict_fingerprint
// is a GENERATED column and Postgres evaluates a statement's generated columns from THAT SAME
// statement's other new values — verified live against the running dev database: a single UPDATE
// that sets both from_verification_state AND from_warning_acknowledged_for = from_verdict_fingerprint
// in one statement reads the OLD (pre-statement) fingerprint, silently pairing the acknowledgment
// with the verdict that is about to be REPLACED. A second statement issued AFTER the first commits
// its verdict columns sees the correct, just-written fingerprint.
//
// aerr is non-nil (and the slice is nil) exactly when the verdict is WARN and unacknowledged
// (M3-P4-OD-15) — the caller must refuse the WHOLE write.
//
// A caller must only invoke this after the gate genuinely ran (a fromGateResult from runFromGate)
// — a zero-value fromGateResult (Outcome=="") is treated the SAME as OutcomeIndeterminate
// (write nothing) rather than falling through to write an empty string into the
// from_verification_state enum column, which is the failure mode a call site that forgets to
// guard "did the gate actually run this time" would otherwise hit.
func fromGateVerdictSets(res fromGateResult) (sets []fromGateColumnSet, aerr *apiError) {
	v := res.verdict
	if v.Outcome == fromgate.OutcomeIndeterminate || v.Outcome == "" {
		return nil, nil
	}
	if res.acknowledgeRequired {
		return nil, newErr(codeUnprocessable, "from_alignment_warning_unacknowledged").
			withDetails(fieldDetail{Field: acknowledgeAlignmentWarningField, Issue: issueRequired})
	}
	detailJSON, _ := json.Marshal(v.Detail)
	var dmarcPolicy *string
	if v.DMARCPolicy != "" {
		dmarcPolicy = &v.DMARCPolicy
	}
	return []fromGateColumnSet{
		{"from_verification_state", v.State},
		{"from_dmarc_policy", dmarcPolicy},
		{"from_spf_aligned", v.SPFAligned},
		{"from_dmarc_aligned", v.DMARCAligned},
		{"from_check_detail", string(detailJSON)},
		{"from_checked_at", nowLiteral{}},
	}, nil
}

// fromGateWriteAcknowledgment issues the SECOND statement (§ above) that sets
// from_warning_acknowledged_at/_for for a row whose FIRST statement (the INSERT or the verdict-
// column UPDATE) has already committed within the same transaction. Called only when
// res.verdict.Outcome == OutcomeWarn and acknowledged == true; a caller must not call this for
// ALLOW/BLOCK/indeterminate outcomes (aligned/blocked never carry an acknowledgment;
// M3-P4-OD-9's override path is a distinct, operator-only mechanism, not this one).
func fromGateWriteAcknowledgment(ctx context.Context, db pgxDB, accountID string) error {
	_, err := db.Exec(ctx,
		`UPDATE send_byo.accounts SET from_warning_acknowledged_at = NOW(),
		   from_warning_acknowledged_for = from_verdict_fingerprint
		 WHERE id = $1`, accountID)
	return err
}
