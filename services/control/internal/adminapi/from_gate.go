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

	"github.com/coldmail-send-byo/control/internal/fromgate"
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

// fromGateOptions resolves §6.1's seven config dials fresh from send_byo.config, mirroring
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
		`UPDATE send_byo.accounts SET from_warning_acknowledged_at = NOW(),
		   from_warning_acknowledged_for = from_verdict_fingerprint
		 WHERE id = $1`, accountID)
	return err
}

// fromGateAcknowledgeStored (#141) records the acknowledgment against the verdict ALREADY STORED on
// the row, for a PATCH that supplies acknowledge_alignment_warning WITHOUT changing the address.
// The contract says an unacknowledged account "stays non-routable until a later Test/PATCH supplies the
// acknowledgment" — but the only ack write on this route was gated on the address CHANGING, so a
// PATCH that resent the same address (or sent only the flag) was accepted, returned 200, and recorded
// nothing. It writes only where there is something to acknowledge (an unaligned_warned row) and reports
// whether it did; an aligned/blocked/unverified row has no outstanding warning and is left alone.
func fromGateAcknowledgeStored(ctx context.Context, db pgxDB, accountID string) (bool, error) {
	tag, err := db.Exec(ctx,
		`UPDATE send_byo.accounts SET from_warning_acknowledged_at = NOW(),
		   from_warning_acknowledged_for = from_verdict_fingerprint
		 WHERE id = $1 AND from_verification_state = 'unaligned_warned'`, accountID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// acknowledgeAlignmentWarningField mirrors platformapi's own constant (duplicated per OD-7).
const acknowledgeAlignmentWarningField = "acknowledge_alignment_warning"

// overrideFromGateColumnSets builds the from_*-column fragment for M3-P4-OD-9's break-glass
// override: writes from_verification_state = 'unverified' (never 'aligned', never
// 'unaligned_warned', even for an address that would genuinely pass — "the override buys the
// operator the ability to STORE the address, not the ability to SEND from it") AND resets
// from_checked_at to NULL, because send_byo_0026's accounts_from_checked_pairing_check makes those
// two columns a PAIR:
//
//	CHECK ((from_verification_state =  'unverified' AND from_checked_at IS     NULL)
//	    OR (from_verification_state <> 'unverified' AND from_checked_at IS NOT NULL))
//
// from_checked_at was ORIGINALLY left untouched here on the reasoning that a stale timestamp is
// diagnostic (it dates the LAST real check). That reasoning is unsatisfiable: on any account whose
// From address has EVER been checked, from_checked_at is non-NULL, so writing 'unverified' alone
// violates the pairing CHECK and Postgres rejects the whole statement. The break-glass override was
// therefore impossible on exactly the accounts that need it — every account requiring an override
// has, by definition, already been checked. Found by walking Stage 5.10 of the local test guide
// (2026-08-03); the only prior test drove POST on a brand-new row, where from_checked_at is already
// NULL and the pairing holds by accident.
//
// The diagnostic history is NOT lost: writeFromGateOverrideEvent records a dedicated
// 'from_gate_overridden' account_events row (address, operator reason, timestamp), and the prior
// verdict's from_dmarc_policy / from_spf_aligned / from_dmarc_aligned / from_check_detail columns
// are still left UNTOUCHED — only the one column the CHECK binds to the state is reset.
func overrideFromGateColumnSets() []fromGateColumnSet {
	return []fromGateColumnSet{
		{"from_verification_state", "unverified"},
		// nil (not nowLiteral) → an explicit NULL bind, satisfying the pairing CHECK's
		// 'unverified' arm. On the INSERT path this is a no-op restatement of the column
		// default; on the UPDATE path it is the fix.
		{"from_checked_at", nil},
	}
}

// writeFromGateOverrideEvent records M3-P4-OD-9's own distinct audit event
// ('from_gate_overridden', send_byo_0041) — its own action value, not folded into 'created'/
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
