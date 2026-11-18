package platformapi

// host_guard.go — the ADD-TIME half of the SMTP host destination guard (Phase 3 close-out C4) on the
// user surface. The decision logic and the blocked-range table are internal/hostguard's; the connect-time
// half, which cannot be raced, is the worker's (services/worker/src/destguard.js). Every writer of
// `smtp_host` on this surface passes through checkSMTPHost: create, bulk-add (per row) and PATCH. The
// operator surface (internal/adminapi) has its own copy of this small wrapper (OD-7: no cross-package
// import between the two API packages) over the SAME hostguard.
//
// WHAT IT DOES. A literal address in any spelling is judged; a name is resolved with the process's own
// resolver and refused if ANY address it resolves to is refused; a name that does not resolve is ALLOWED
// (this module has never resolved at add time, and a host whose DNS has not propagated is a normal thing
// to add — the worker re-checks every name at every connect, so nothing unresolvable-now is trusted
// later). The refusal is a field validation, so it is the standard 400 `validation_failed` with
// `details:[{smtp_host, forbidden_value}]` — the SAME envelope every other invalid field uses; what was
// refused and why (the address, the range) goes to the log only.
//
// THE ALLOWLIST. `smtp_host_allow_cidrs` (default `[]`) names ranges that are admitted for a test harness
// whose SMTP sink lives on a private network. It is read fresh on every check; an unreadable or
// unparseable value is treated as EMPTY (fail closed: the guard stays on), and said in the log. A
// non-empty value is logged at WARN on every use of this route that sees it, and at Control start.

import (
	"context"
	"log"
	"net/netip"
	"time"

	"github.com/coldmail-send-byo/control/internal/hostguard"
)

// hostLookup resolves a host name at add time. A variable so a test can stand a fake resolver in for DNS
// (the package's tests never need the network); production always uses the process's resolver.
var hostLookup hostguard.Lookup = hostguard.SystemLookup

// issueForbiddenValue is the details[].issue for a field whose value is well-formed but not permitted
// (the same word the operator surface already uses). Additive to this surface's issue vocabulary.
const issueForbiddenValue = "forbidden_value"

// hostAllowlist reads smtp_host_allow_cidrs fresh. ok=false means the value could not be used and the
// allowlist is EMPTY (fail closed).
func (s *Server) hostAllowlist(ctx context.Context) []netip.Prefix {
	var raw []byte
	if err := s.db.QueryRow(ctx, `SELECT value FROM send_byo.config WHERE key = 'smtp_host_allow_cidrs'`).Scan(&raw); err != nil {
		log.Printf("platformapi: smtp_host_allow_cidrs unreadable (%v): the SMTP destination guard has NO allowlist level=error", err)
		return nil
	}
	allow, err := hostguard.ParseAllowListJSON(raw)
	if err != nil {
		log.Printf("platformapi: smtp_host_allow_cidrs is not a valid CIDR list (%v): treated as EMPTY, the guard is fully on level=error", err)
		return nil
	}
	if len(allow) > 0 {
		log.Printf("platformapi: WARNING smtp_host_allow_cidrs is non-empty (%s): the SMTP destination guard is relaxed for these ranges; this is a test-harness setting and production leaves it empty level=warn", hostguard.FormatAllowList(allow))
	}
	return allow
}

// checkSMTPHost is the add-time check: nil means the host may be stored. A refusal is an
// apiError(validation_failed) carrying `{smtp_host, forbidden_value}`; its internal reason names the
// host, the address and the range (log only).
func (s *Server) checkSMTPHost(ctx context.Context, host string) *apiError {
	allow := s.hostAllowlist(ctx)
	lctx, cancel := context.WithTimeout(ctx, s.hostLookupTimeout(ctx))
	defer cancel()
	res, err := hostguard.CheckHost(lctx, host, allow, hostLookup)
	if err != nil {
		return newErr(codeValidationFailed, "smtp_host_destination_refused: "+err.Error()).
			withDetails(fieldDetail{Field: "smtp_host", Issue: issueForbiddenValue})
	}
	if res.Unresolved {
		log.Printf("platformapi: smtp_host %q did not resolve at add time (%v): allowed, and re-checked at every connect level=info", host, res.LookupErr)
	}
	return nil
}

// hostLookupTimeout bounds the add-time resolution by the module's own DNS lookup timeout
// (`from_dns_lookup_timeout_ms`, the dial the From gate already uses for every lookup it makes): one
// timeout for "a DNS lookup this module waits for", not a second literal. A read failure falls back to
// the seeded default of that dial.
func (s *Server) hostLookupTimeout(ctx context.Context) time.Duration {
	ms, ok, err := s.readConfigInt(ctx, "from_dns_lookup_timeout_ms")
	if err != nil || !ok || ms <= 0 {
		ms = 3000 // send_byo_0039's seeded default, used only when the row is unreadable
	}
	return time.Duration(ms) * time.Millisecond
}
