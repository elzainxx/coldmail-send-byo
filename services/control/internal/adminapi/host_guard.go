package adminapi

// host_guard.go — the ADD-TIME half of the SMTP host destination guard (Phase 3 close-out C4) on the
// OPERATOR surface. The admin account routes are a permanent parallel write path to `smtp_host`
// (CLAUDE.md, "the admin bypass surface"): a rule that guards only the user routes is a rule the operator
// surface walks around, so create and PATCH here go through the SAME internal/hostguard decision as
// internal/platformapi's host_guard.go (a separate copy of this small wrapper, not an import: OD-7). There
// is no operator override: the one deliberate hole is the allowlist dial `smtp_host_allow_cidrs`, which
// applies to both surfaces and to the worker's connect-time check alike.

import (
	"context"
	"log"
	"net/netip"
	"time"

	"github.com/coldmail-send-byo/control/internal/hostguard"
)

// hostLookup resolves a host name at add time; a variable so a test can stand a fake resolver in for DNS.
var hostLookup hostguard.Lookup = hostguard.SystemLookup

// hostAllowlist reads smtp_host_allow_cidrs fresh; an unreadable or invalid value is EMPTY (fail closed).
func (s *Server) hostAllowlist(ctx context.Context) []netip.Prefix {
	var raw []byte
	if err := s.db.QueryRow(ctx, `SELECT value FROM send_byo.config WHERE key = 'smtp_host_allow_cidrs'`).Scan(&raw); err != nil {
		log.Printf("adminapi: smtp_host_allow_cidrs unreadable (%v): the SMTP destination guard has NO allowlist level=error", err)
		return nil
	}
	allow, err := hostguard.ParseAllowListJSON(raw)
	if err != nil {
		log.Printf("adminapi: smtp_host_allow_cidrs is not a valid CIDR list (%v): treated as EMPTY, the guard is fully on level=error", err)
		return nil
	}
	if len(allow) > 0 {
		log.Printf("adminapi: WARNING smtp_host_allow_cidrs is non-empty (%s): the SMTP destination guard is relaxed for these ranges; this is a test-harness setting and production leaves it empty level=warn", hostguard.FormatAllowList(allow))
	}
	return allow
}

// checkSMTPHost is the add-time check: nil means the host may be stored. A refusal is a 400
// validation_failed with details [{smtp_host, forbidden_value}]; the address and range that were
// refused are in the internal reason (log only). A name that does not resolve is allowed (see
// internal/hostguard: the worker re-checks every name at every connect).
func (s *Server) checkSMTPHost(ctx context.Context, host string) *apiError {
	allow := s.hostAllowlist(ctx)
	timeout := 3 * time.Second
	if ms, err := readConfigInt(ctx, s.db, "from_dns_lookup_timeout_ms"); err == nil && ms > 0 {
		timeout = time.Duration(ms) * time.Millisecond
	}
	lctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := hostguard.CheckHost(lctx, host, allow, hostLookup)
	if err != nil {
		return newErr(codeValidationFailed, "smtp_host_destination_refused: "+err.Error()).
			withDetails(fieldDetail{Field: "smtp_host", Issue: issueForbiddenValue})
	}
	if res.Unresolved {
		log.Printf("adminapi: smtp_host %q did not resolve at add time (%v): allowed, and re-checked at every connect level=info", host, res.LookupErr)
	}
	return nil
}
