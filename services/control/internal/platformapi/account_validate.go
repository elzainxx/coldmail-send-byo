package platformapi

import (
	"net"
	"regexp"
	"strings"
)

// Account field validation mirrors the Phase 1 CHECK constraints (same shape as adminapi's own
// validators — OD-7 isolation, duplicated not imported) so the API rejects with a clean envelope
// BEFORE the DB; the CHECK remains the backstop.

var fromAddressRe = regexp.MustCompile(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$`)
var hostnameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.\-]*[A-Za-z0-9])?$`)

func validateSMTPHost(host string) *fieldDetail {
	if host == "" || len(host) > 255 {
		return &fieldDetail{Field: "smtp_host", Issue: issueMalformed}
	}
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

// dailyLimitCeiling mirrors adminapi's DailyLimitCeiling (OD-24) — a hardcoded sanity BOUND, not
// a tuning dial (a fat-finger guard, not a config-not-code violation).
const dailyLimitCeiling = 100_000

func validateDailyLimit(limit int) *fieldDetail {
	if limit <= 0 || limit > dailyLimitCeiling {
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
