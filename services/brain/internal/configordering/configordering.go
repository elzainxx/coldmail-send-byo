// Package configordering holds the two documented config-ordering invariants (CLAUDE.md Rule 1;
// Phase 7 §5.3 authoritative) as pure, dependency-free functions — no DB import, no HTTP/error-
// envelope knowledge. This is the SINGLE source of truth both enforcement points call, so the
// admin PUT boundary (internal/adminapi) and the config-LOAD path (internal/dispatch) can never
// drift onto two different definitions of the same invariant (P9-OD-2, RULED-AND-CLOSED).
package configordering

import "fmt"

// Violation describes one invariant breach: the key being set/loaded, its candidate value, the
// counterpart key's name and CURRENT value, and the constraint verb ("must be <" / "must be >=").
// Callers render this however their own error-envelope convention requires.
type Violation struct {
	Key              string
	Value            int
	Constraint       string // "must be <" or "must be >="
	CounterpartKey   string
	CounterpartValue int
}

// Detail renders the violation WITHOUT the "config_ordering_violation: " prefix — the exact
// format the config-LOAD path has always used internally (preserved byte-for-byte so existing
// tests asserting that string keep passing unchanged).
func (v *Violation) Detail() string {
	return fmt.Sprintf("%s (%d) %s %s (%d)", v.Key, v.Value, v.Constraint, v.CounterpartKey, v.CounterpartValue)
}

func (v *Violation) Error() string { return "config_ordering_violation: " + v.Detail() }

// CheckSMTPTimeoutVsLease enforces invariant (a), P6-11: smtp_operation_timeout_sec must be
// STRICTLY less than job_max_processing_time_sec, so an account hang trips the box's own
// per-operation timeout (a scored, box-reported failure) before the job lease expires (an
// unscored Brain reclaim that would thrash). Equality is also a violation.
func CheckSMTPTimeoutVsLease(smtpTimeoutSec, leaseSec int) *Violation {
	if smtpTimeoutSec >= leaseSec {
		return &Violation{
			Key: "smtp_operation_timeout_sec", Value: smtpTimeoutSec, Constraint: "must be <",
			CounterpartKey: "job_max_processing_time_sec", CounterpartValue: leaseSec,
		}
	}
	return nil
}

// CheckBufferTTLVsReconnectWindow enforces invariant (b), P7-11: box_outcome_buffer_ttl_sec must
// be greater than or equal to startup_reconnect_window_sec, so a box reconnecting late-but-in-
// window still finds its buffered outcome (else the outcome already expired -> a silent
// duplicate on reclaim-and-resend). Bare equality is the floor, not itself a violation.
func CheckBufferTTLVsReconnectWindow(bufferTTLSec, reconnectWindowSec int) *Violation {
	if bufferTTLSec < reconnectWindowSec {
		return &Violation{
			Key: "box_outcome_buffer_ttl_sec", Value: bufferTTLSec, Constraint: "must be >=",
			CounterpartKey: "startup_reconnect_window_sec", CounterpartValue: reconnectWindowSec,
		}
	}
	return nil
}

// CheckFromDNSLookupTimeoutVsBudget enforces Module 3 Phase 4 §6.4's invariant: the per-lookup
// timeout must not EXCEED the whole-check budget — STRICT ceiling honored as `<=` (equality is
// the boundary, not itself a violation, per Phase 4 §11 T11: "the valid boundary case (==) is
// accepted"). A budget below a single lookup's timeout makes EVERY From-gate check fail
// indeterminate, presenting as "account creation is broken" with no diagnosable cause — the
// Phase 8 HIGH-finding class this rule exists to keep out of config writes (CLAUDE.md Rule 11).
func CheckFromDNSLookupTimeoutVsBudget(lookupTimeoutMS, totalBudgetMS int) *Violation {
	if lookupTimeoutMS > totalBudgetMS {
		return &Violation{
			Key: "from_dns_lookup_timeout_ms", Value: lookupTimeoutMS, Constraint: "must be <=",
			CounterpartKey: "from_dns_total_budget_ms", CounterpartValue: totalBudgetMS,
		}
	}
	return nil
}

// CheckFromDNSNegativeCacheVsPositiveCache enforces Phase 4 §6.5's asymmetric-caching invariant:
// the NEGATIVE (absence) cache TTL must not EXCEED the positive one. A stale negative is a cached
// decision to be MORE PERMISSIVE — "Leniency-by-caching is the dangerous direction" (§6.5) — so a
// negative TTL exceeding the positive one silently inverts the safety asymmetry the two separate
// dials exist to encode. Equality is the boundary, not a violation (mirrors T11's `==` acceptance).
func CheckFromDNSNegativeCacheVsPositiveCache(negativeTTLSec, positiveTTLSec int) *Violation {
	if negativeTTLSec > positiveTTLSec {
		return &Violation{
			Key: "from_dns_negative_cache_ttl_sec", Value: negativeTTLSec, Constraint: "must be <=",
			CounterpartKey: "from_dns_cache_ttl_sec", CounterpartValue: positiveTTLSec,
		}
	}
	return nil
}

// CheckQuarantineMinSample enforces Module 3 Phase 5 §10.4's invariant: quarantine_min_sample must
// be at least 1. A zero min-sample would let the failure-rate trigger (§1.4 branch 3) fire on the
// very FIRST failure — silently converting a windowed rate trigger into a one-strike trigger, and
// making the consecutive-failure threshold (branch 2) unreachable in practice, since the rate
// branch would always win first. There is no counterpart key for this invariant — it is a
// single-key floor, so Violation's CounterpartKey/CounterpartValue are left at their zero values
// and the caller renders Detail() accordingly (the format still reads correctly with a 0 zero
// value, since the constraint text names the floor explicitly).
func CheckQuarantineMinSample(minSample int) *Violation {
	if minSample < 1 {
		return &Violation{
			Key: "quarantine_min_sample", Value: minSample, Constraint: "must be >=",
			CounterpartKey: "1 (a fixed floor, not a counterpart config key)", CounterpartValue: 1,
		}
	}
	return nil
}

// CheckQuarantineFailureRatePct enforces Phase 5 §10.4's invariant: quarantine_failure_rate_pct
// must be within [1,100]. A rate of 0 would quarantine every account with any failure history at
// all (0% is satisfied trivially); above 100 is meaningless as a percentage and could never fire,
// silently disabling the rate trigger while `config_drift` still reports the key as configured —
// exactly the "false promise of control" defect class this invariant exists to keep out of a
// routine admin write (CLAUDE.md Rule 11 / Phase 8 R3/R4 precedent).
func CheckQuarantineFailureRatePct(ratePct int) *Violation {
	if ratePct < 1 || ratePct > 100 {
		return &Violation{
			Key: "quarantine_failure_rate_pct", Value: ratePct, Constraint: "must be within [1,100]",
			CounterpartKey: "[1,100] (a fixed range, not a counterpart config key)", CounterpartValue: 0,
		}
	}
	return nil
}

// CheckDelayOrder enforces Module 3 Phase 6 §4.4/M3-P1-OD-8's resolved-set ordering invariant:
// send_delay_min_sec must not exceed send_delay_max_sec IN THE RESOLVED PAIR (per-user override ∪
// global), not merely within one row. A per-user override table with no row-level view of the
// global counterpart (or vice versa) can satisfy every individual CHECK constraint while the
// resolved pair is inverted — the exact gap `user_config_overrides_delay_order_check` cannot close
// on its own (Phase 1 §4.2's own documented limitation). Both callers (an override write validating
// against the live globals, and a global write validating against every existing override row)
// reduce to this one pairwise check, so the two directions can never define "inverted" differently.
func CheckDelayOrder(minSec, maxSec int) *Violation {
	if minSec > maxSec {
		return &Violation{
			Key: "send_delay_min_sec", Value: minSec, Constraint: "must be <=",
			CounterpartKey: "send_delay_max_sec", CounterpartValue: maxSec,
		}
	}
	return nil
}
