package adminapi

import "regexp"

// Account field validation (§3) mirrors the Phase 1 CHECK constraints so the API rejects with a
// clean envelope BEFORE the DB (the CHECK remains the backstop). Each returns a fieldDetail
// (nil if valid) so a handler can collect all field issues into one validation_failed envelope.

// fromAddressRe matches the Phase 1 accounts_from_address_valid_check regex exactly.
var fromAddressRe = regexp.MustCompile(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$`)

// hostRe: a hostname label sequence or an IP-ish literal. No scheme, no port, no path (§3). This
// is a SYNTACTIC check only — a live SMTP connectivity probe is DEFER-TO-PHASE-3 (OD-8).
var hostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.\-:]*[A-Za-z0-9])?$`)

func validateSMTPHost(host string) *fieldDetail {
	if host == "" || len(host) > 255 || !hostRe.MatchString(host) {
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
