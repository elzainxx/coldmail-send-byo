package dispatch

// dest_guard.go — the Control's half of the connect-time SMTP destination guard (Phase 3 close-out C4).
// The Control does not dial a user's SMTP host; the worker does, and it refuses a loopback, private,
// link-local or metadata destination (services/worker/src/destguard.js). The one thing the worker cannot
// know is the allowlist (`smtp_host_allow_cidrs`, default empty): it has no database, so the Control sends
// it on every push_job and push_verify frame. A value that does not parse is sent as EMPTY — the guard
// stays fully on — and said in the log; it never reaches the worker half-understood.

import (
	"context"
	"log"

	"github.com/coldmail-send-byo/control/internal/hostguard"
	"github.com/jackc/pgx/v5/pgxpool"
)

// allowCIDRsForWire returns the allowlist to put on a frame: the stored CIDRs when every entry parses,
// else nil (EMPTY), with the reason logged.
func allowCIDRsForWire(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	if _, err := hostguard.ParseAllowList(raw); err != nil {
		log.Printf("dispatch: smtp_host_allow_cidrs is not a valid CIDR list (%v): sending an EMPTY allowlist, the worker's destination guard stays fully on level=error", err)
		return nil
	}
	return raw
}

// verifyAllowCIDRs reads the allowlist fresh for a verification (Verify has no Config: it is called from the
// request path). An unreadable row is EMPTY.
func verifyAllowCIDRs(ctx context.Context, db *pgxpool.Pool) []string {
	raw, err := readStringArrayKey(ctx, db, "smtp_host_allow_cidrs")
	if err != nil {
		log.Printf("dispatch: smtp_host_allow_cidrs unreadable for a verification (%v): sending an EMPTY allowlist level=error", err)
		return nil
	}
	return allowCIDRsForWire(raw)
}
