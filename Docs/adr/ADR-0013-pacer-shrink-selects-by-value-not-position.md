# ADR-0013: the pacer's ceiling-shrink normalization selects lanes to keep BY VALUE, not by slice position

- **Status:** accepted
- **Date:** 2026-08-02
- **Supersedes:** —
- **Superseded-by:** —

## Context

Module 3 Phase 6 (§3.3) makes the per-user dispatch concurrency ceiling admin-overridable, which
turns a rare, almost-never-exercised code path — `pacer.normalize`'s ceiling-shrink branch
(`services/brain/internal/dispatch/pacing.go`) — into a routine one: an admin lowering one user's
concurrency is now an ordinary support action, not a global config change nobody ever actually
makes.

`normalize`'s own doc comment, inherited byte-for-byte from module 2, has always read:

> Extra slots (a lowered ceiling) are dropped from the front (the most-cooled-down), keeping the
> longest-cooling timers.

The code beneath that comment did not implement it: `slots = slots[len(slots)-ceiling:]` drops from
the front of the underlying **slice**, but slice position carries no readiness meaning. `Stamp`
advances *the earliest* lane's timer and writes it back **in place** (`pacing.go`), so which index in
the slice holds the longest-cooling timer at any given moment is an accident of prior resizes and
stamps, not an ordering the code establishes or preserves. Front-drop and "keep the longest-cooling
timers" are two different rules that happened to never be exercised together often enough, at low
ceiling-shrink frequency, for the mismatch to matter.

Phase 6's own §13.4 review (a scoped agent pass, "can a concurrent override change produce an
inconsistent effective value mid-dispatch?") confirmed the mismatch directly against the tree and
sharpened it further: dropping a lane that still owes cooldown **forgives** that cooldown (a shrink
that discards a long-cooling lane and keeps an already-ready one moves the user's *effective* pacing
faster than the resolved config intends); dropping an already-ready lane costs nothing. Forgiving is
therefore the unsafe direction — the same principle CLAUDE.md's Phase 4 negative-cache-TTL ruling
already established for a different subsystem.

## Decision

**On a ceiling shrink, `normalize` keeps the `ceiling` lanes that owe the MOST cooldown (the latest
next-eligible times) and drops the ones that are already most-ready — selected by comparing the
stored `time.Time` VALUES, never by slice index.**

This is a **bug fix, not a behavior change**. The comment above `normalize` was aspirational, not
descriptive, from the moment it was written in module 2: it stated the intended contract, and the
code beneath it never matched that contract. Module 3 is not deciding a new policy here — it is
making the code do what its own comment already claimed. A reader diffing module 2 and module 3
against each other should read this as module 3 correcting a latent defect, not as module 3
overriding a deliberate module-2 decision.

Implementation: on shrink, `normalize` sorts a **copy** of the register descending by next-eligible
time (latest/most-cooling first) and keeps the first `ceiling` entries. The register is
`ceiling`-sized (single digits in practice) and `normalize` already runs under the pacer's own
mutex, so the sort costs nothing measurable. The doc comment above the function is left as-is (it
was already correct); only the function body changed.

## Consequences

- A user whose concurrency is lowered mid-cooldown keeps exactly the cooldowns they were already
  serving — none are silently forgiven, none are silently invented. The alternative (front-drop)
  could let a user burst sends immediately after a support-initiated slowdown, which is the opposite
  of what the operator action intended.
- The fix is exercised by `TestPacerShrink_T7d_KeepsTheLanesOwingTheMostCooldown`
  (`services/brain/internal/dispatch/pacer_shrink_test.go`), with
  `TestPacerShrink_T7dControl_TheOldFrontDropFailsThisAssertion` as the paired negative control — the
  pre-fix front-drop, applied to the identical seeded register, must (and does) fail the same
  "every surviving lane owes at least as much cooldown as every dropped lane" assertion. Without that
  control, a test asserting only the NEW behavior could not distinguish "the fix works" from "the
  test happens to pass regardless."
- `TestPerTick_T7_ResolutionIsConsistentWithinOneTick` and its own required control
  (`TestPerTick_T7Control_ResolvingPerSiteCorruptsTheRegister`) additionally prove the RELATED, but
  distinct, per-tick-consistency property this same ceiling-mutability exposes: resolving the
  effective ceiling more than once within a single tick (rather than once, threaded by parameter)
  corrupts the register the same way a wrong shrink rule would, via the register's own grow path.
- No schema change: this ruling selects among lanes already held in the pacer's in-memory register;
  it persists nothing and adds no migration.

## Links

- CLAUDE.md Rule 10 (anti-vacuity for concurrency tests) — the paired negative control this ADR's
  test suite provides.
- `Docs/history/phase-6-per-user-config-and-routing.md` §3.3 (M3-P6-OD-1's ruling text) and §9.1 (the
  scoped review pass that surfaced the grow-direction defect and the three-item in-memory-state
  inventory).
- `Docs/open-decisions.md` — M3-P6-OD-1.
- `services/brain/internal/dispatch/pacing.go` (`normalize`), `pacer_shrink_test.go`,
  `pertick_resolution_test.go`.
