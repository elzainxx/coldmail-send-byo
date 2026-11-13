# ADR-0015: `contract_drift.sh` extends to enumerate the platform-surface account routes

- **Status:** accepted
- **Date:** 2026-07-31
- **Supersedes:** —
- **Superseded-by:** —

## Context

`tests/harness/checks/contract_drift.sh` is a BLOCKING CI gate: it asserts the Brain SERVES exactly
the routes documented in `Docs/api-contract.md`, and no undocumented ones. Before Phase 3, its only
enumerated surface was `ADMIN_ROUTES` — the `/v1/admin/*` operator surface (Phase 2 §1.3 separately
found and fixed an off-by-one in that array's own printed count, logged in the shared-code manifest
as a module-2 bug that does not get back-ported).

Phase 3 adds the first **user-facing** platform-surface routes (`platformapi`, §1.1) — routes a
tenant reaches directly, not through an operator bearer key. `contract_drift.sh`'s array was
admin-only, so these new routes had no gate enumerating them at all: a route the gate does not know
about is a route whose authorization nobody mechanically checks on every push. This is the identical
failure shape the Phase 2 off-by-one exposed — *a route that no gate enumerates is a route whose
auth nobody checks* — just on a different surface.

Extending a BLOCKING CI gate's own enumerated surface is a discipline change, not a routine test
addition: CLAUDE.md Rule 8 requires newly locked invariants to be appended by reviewed decision,
never introduced quietly inside an unrelated feature commit. The ruling's own text (§9.4) required
this ADR as part of landing the change — this document is that ADR.

## Decision

**`contract_drift.sh` gains a second enumerated array, `PLATFORM_ROUTES`, alongside the existing
`ADMIN_ROUTES` — every `/v1/byosmtp/*` (and later, `/v1/quote`/`/v1/run`/`/v1/report`-family)
platform-surface route the Brain serves is enumerated and probed the same way the admin surface
already is: each must be documented in `Docs/api-contract.md`, and each undocumented-but-served
route fails the gate.**

This is additive to the gate's existing behavior, not a replacement — `ADMIN_ROUTES` is untouched,
and the two arrays are checked independently so a regression on one surface does not mask a
regression on the other.

## Consequences

- Every phase that adds a platform-surface route (Phase 3 onward) must add it to `PLATFORM_ROUTES`
  in the same change that adds the handler, exactly as `ADMIN_ROUTES` already requires for admin
  routes — the gate fails otherwise, by construction.
- `contract_drift.sh`'s printed counts (admin route count, platform route count) are now genuinely
  asserted against the array lengths for both surfaces, closing the same "printed as decoration,
  nothing fails on it" gap Phase 2 found and fixed for the admin count alone.
- Phase 6 later added its own routes (2 admin, 2 platform) to these same arrays without needing a
  fresh ADR — this decision covers the mechanism, not each route added through it.
- No schema change, no config key: this ADR governs a CI gate's coverage, not runtime behavior.
- **This ADR was written 2026-08-02, two days after Phase 3 actually implemented and landed the
  `PLATFORM_ROUTES` extension (2026-07-31).** Found missing by Phase 6's mechanical landing check
  (the ruling's own text required an ADR under Rule 8, and the gate extension landed while the ADR
  did not). Recorded here as a backfill of a decision that was already correctly implemented and
  already enforcing in CI — not a design change.

## Links

- CLAUDE.md Rule 8 (newly locked invariants are appended by reviewed decision).
- `Docs/history/phase-3-user-owned-accounts.md` §9.4 and §11 (M3-P3-OD-11's ruling text).
- `Docs/open-decisions.md` — M3-P3-OD-11.
- `Docs/architecture.md#relationship-to-module-2-the-shared-code-manifest` entry 1 (the Phase 2 admin-route off-by-one this gate's own history
  traces back to).
- `tests/harness/checks/contract_drift.sh` (`PLATFORM_ROUTES` array, the enforcement point).
