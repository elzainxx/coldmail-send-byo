// from_gate.go — Module 3 Phase 4 §2.8/M3-P4-OD-9's store-layer gate, applied to the TWO
// operator-surface writers of from_address (POST /v1/admin/accounts, PATCH /v1/admin/accounts/{id}).
// §2.8 found these were the LIVE bypass: they wrote from_address without ever touching
// from_verification_state, so a PATCH on this surface could preserve a stale 'aligned' verdict
// attached to an address the gate never saw. M3-P4-OD-9 ruled: the gate runs here too, and the
// ONE way past it is an explicit, audited override flag with a required reason — never a silent
// reset to 'unverified'.
package adminapi

import (
	"context"
	"encoding/json"
	"time"

	"github.com/byosmtp-module/brain/internal/fromgate"
	"github.com/jackc/pgx/v5"
)

// fromGateResult mirrors platformapi's own type (duplicated per OD-7 isolation).
type fromGateResult struct {
	verdict             fromgate.Verdict
	acknowledgeRequired bool
}

// runFromGate evaluates the From alignment gate — the SAME evaluator platformapi uses
// (internal/fromgate has no internal package imports, so both surfaces may hold a reference
// directly; unlike internal/dispatch, this is not an OD-7-restricted package).
func (s *Server) runFromGate(ctx context.Context, fromAddress, smtpHost, smtpUsername string, acknowledged bool) fromGateResult {
	opts := s.fromGateOptions(ctx)
	v := fromgate.Evaluate(ctx, fromAddress, smtpHost, smtpUsername, opts)
	return fromGateResult{
		verdict:             v,
		acknowledgeRequired: v.Outcome == fromgate.OutcomeWarn && !acknowledged,
	}
}

// fromGateOptions resolves §6.1's seven config dials fresh from byosmtp.config, mirroring
// platformapi's own fromGateOptions (duplicated per OD-7; both packages read the SAME global
// config keys, since the gate's dials are operator-wide, not per-surface).
func (s *Server) fromGateOptions(ctx context.Context) fromgate.Options {
	mode, err := readConfigString(ctx, s.db, "from_gate_mode")
	if err != nil {
		mode = "enforce"
	}
	resolverAddr, err := readConfigString(ctx, s.db, "from_dns_resolver")
	if err != nil {
		resolverAddr = ""
	}
	lookupMS, err := readConfigInt(ctx, s.db, "from_dns_lookup_timeout_ms")
	if err != nil {
		lookupMS = 3000
	}
	budgetMS, err := readConfigInt(ctx, s.db, "from_dns_total_budget_ms")
	if err != nil {
		budgetMS = 15000
	}
	posture, err := readConfigString(ctx, s.db, "from_spf_lookup_limit_posture")
	if err != nil {
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

// fromGateColumnSet/nowLiteral mirror platformapi's own types (duplicated per OD-7 isolation).
type fromGateColumnSet struct {
	Col string
	Val any
}
type nowLiteral struct{}

// fromGateVerdictSets mirrors platformapi's own function byte-for-byte in behavior (duplicated
// per OD-7 isolation) — see that package's copy for the full self-reference-hazard rationale
// behind excluding the acknowledgment pair from this list.
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

// fromGateWriteAcknowledgment mirrors platformapi's own function (duplicated per OD-7 isolation).
func fromGateWriteAcknowledgment(ctx context.Context, db pgxDB, accountID string) error {
	_, err := db.Exec(ctx,
		`UPDATE byosmtp.accounts SET from_warning_acknowledged_at = NOW(),
		   from_warning_acknowledged_for = from_verdict_fingerprint
		 WHERE id = $1`, accountID)
	return err
}

// acknowledgeAlignmentWarningField mirrors platformapi's own constant (duplicated per OD-7).
const acknowledgeAlignmentWarningField = "acknowledge_alignment_warning"

// overrideFromGateColumnSets builds the from_*-column fragment for M3-P4-OD-9's break-glass
// override: writes ONLY from_verification_state = 'unverified' (never 'aligned', never
// 'unaligned_warned', even for an address that would genuinely pass — "the override buys the
// operator the ability to STORE the address, not the ability to SEND from it") — every other
// From-gate column (from_dmarc_policy, from_spf_aligned, from_dmarc_aligned, from_check_detail,
// from_checked_at) is left UNTOUCHED, because no check actually ran; a stale prior verdict's
// timestamp remains, which is itself diagnostic (it dates the LAST time a real check happened).
func overrideFromGateColumnSets() []fromGateColumnSet {
	return []fromGateColumnSet{
		{"from_verification_state", "unverified"},
	}
}

// writeFromGateOverrideEvent records M3-P4-OD-9's own distinct audit event
// ('from_gate_overridden', byosmtp_0041) — its own action value, not folded into 'created'/
// 'updated', so "did an operator bypass the From gate on this account" is directly queryable.
// from_flags/to_flags are left nil (the override is not a status-flags transition — the SAME
// fixed flagsSnapshot decode shape every OTHER event on this surface relies on would silently
// zero-value a differently-shaped payload here, the exact hazard M3-P3-OD-15 already found), so
// the address and the operator's reason live in the free-text reason column, matching that
// finding's own resolution.
func writeFromGateOverrideEvent(ctx context.Context, tx pgx.Tx, accountID, userID, fromAddress, overrideReason, requestID string) error {
	reason := "from_gate override: from_address=" + fromAddress + " reason=" + overrideReason
	return writeAuditEvent(ctx, tx, accountID, userID, "from_gate_overridden", nil, nil, &reason, requestID)
}
