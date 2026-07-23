package adminapi

import (
	"net"
	"regexp"
	"strings"
)

// Account field validation (§3) mirrors the Phase 1 CHECK constraints so the API rejects with a
// clean envelope BEFORE the DB (the CHECK remains the backstop). Each returns a fieldDetail
// (nil if valid) so a handler can collect all field issues into one validation_failed envelope.

// fromAddressRe matches the Phase 1 accounts_from_address_valid_check regex exactly.
var fromAddressRe = regexp.MustCompile(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$`)

// hostnameRe: a DNS hostname label sequence — letters/digits/dots/hyphens only, no colon (so a
// ":port" suffix is rejected), no scheme, no path (§3). IP literals are validated separately via
// net.ParseIP so IPv6 (which legitimately contains colons) is still accepted as a bare literal.
// This is a SYNTACTIC check only — a live SMTP connectivity probe is DEFER-TO-PHASE-3 (OD-8).
var hostnameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.\-]*[A-Za-z0-9])?$`)

func validateSMTPHost(host string) *fieldDetail {
	if host == "" || len(host) > 255 {
		return &fieldDetail{Field: "smtp_host", Issue: issueMalformed}
	}
	// Accept a bare IP literal (v4 or v6) OR a DNS hostname. Reject anything with a scheme,
	// path, or a ":port" suffix on a hostname (§3: "no scheme/port/path"). An IPv6 literal is a
	// bare address here (no brackets, no port) — net.ParseIP accepts it; a "host:port" string
	// parses as neither a valid IP nor a hostname, so it is rejected.
	if net.ParseIP(host) != nil {
		return nil
	}
	if strings.ContainsAny(host, ":/\\ @") || !hostnameRe.MatchString(host) {
		return &fieldDetail{Field: "smtp_host", Issue: issueMalformed}
	}
	return nil
}

func validateSMTPPort(port int) *fieldDetail {
	if port < 1 || port > 65535 {
		return &fieldDetail{Field: "smtp_port", Issue: issueOutOfRange}
	}
	return nil
}

func validateSMTPUsername(u string) *fieldDetail {
	if u == "" || len(u) > 320 {
		return &fieldDetail{Field: "smtp_username", Issue: issueMalformed}
	}
	return nil
}

func validateFromAddress(a string) *fieldDetail {
	if a == "" || len(a) > 320 || !fromAddressRe.MatchString(a) {
		return &fieldDetail{Field: "from_address", Issue: issueMalformed}
	}
	return nil
}

func validateFromName(n *string) *fieldDetail {
	if n != nil && len(*n) > 255 {
		return &fieldDetail{Field: "from_name", Issue: issueMalformed}
	}
	return nil
}

// validateDailyLimit checks the > 0 rule (the ≥ daily_sent_count rule on PATCH is handled
// separately as a 409, §3.1).
//
// OD-24 (FLAGGED, deferred): §3/§6.4 also say daily_limit must be "≤ a configurable ceiling — §6",
// but no ceiling config key was ever seeded (Phase 1 §4 has no such key). Per CLAUDE.md Rule 2 we
// do NOT invent one here (that would change the locked Phase 9 §4 reference); the upper bound is
// left unenforced and flagged in Docs/open-decisions.md OD-24 awaiting a ruling on the key. The
// `> 0` lower bound (backstopped by the Phase 1 accounts_daily_limit_check) is enforced.
func validateDailyLimit(limit int) *fieldDetail {
	if limit <= 0 {
		return &fieldDetail{Field: "daily_limit", Issue: issueOutOfRange}
	}
	return nil
}

// collect drops nil details and returns the non-nil ones.
func collect(ds ...*fieldDetail) []fieldDetail {
	var out []fieldDetail
	for _, d := range ds {
		if d != nil {
			out = append(out, *d)
		}
	}
	return out
}
