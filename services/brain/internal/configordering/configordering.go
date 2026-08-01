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
