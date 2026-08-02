# ADR-0014: a `RESERVED` config-consumption marker requires a mechanical, gate-checked expiry

- **Status:** accepted
- **Date:** 2026-07-31
- **Supersedes:** —
- **Superseded-by:** —

## Context

`tests/harness/checks/config_consumption.sh` is a BLOCKING CI gate: every seeded `byosmtp.config`
key must have either a grep-verifiable real consumer, or an explicit `RESERVED` marker in
`Docs/config-consumers.txt` (mirrored by a `RESERVED / NOT-YET-CONSUMED` string in
`Docs/config-reference.md`, so an operator reading the docs — not just the ledger — can see the
dial does nothing yet).

`RESERVED` exists because Phase 2's G1 stage seeds a dial (or, more generally, any phase can
introduce a key ahead of the code that will read it) — but a marker with no expiry is exactly how
two of module 2's own dials, `retry_count_4xx` and `variation_zero_width_rate_pct`, went from
"temporarily unconsumed" to permanently, silently inert: two separate Phase 9.5 review sweeps found
them RESERVED "by accident," with no expiry field and nothing that would ever force the question
back open. A key that is seeded, documented, and passing `config_consumption` is worse than a
hardcoded constant — it is a false promise of control an operator has no way to notice is false.

Module 3's own Phase 2 §1.2.1 needed a fresh instance of exactly this pattern: `G1` seeds a
config-ordering-adjacent dial, `G2` must be the one that gives it a real consumer, and G1's seed
was never meant to survive past G2's own close. The user's own review pass ruled, in substance,
that this must be enforced mechanically rather than left as an unstated intention — this ADR
records that ruling as a locked, appended invariant per CLAUDE.md Rule 8 (newly locked invariants
are appended by reviewed decision, not introduced quietly), since strengthening a BLOCKING CI gate
is exactly the class of change Rule 8 exists to gate.

## Decision

**A `RESERVED` marker in `Docs/config-consumers.txt` MUST carry a third field naming its expiry
gate or phase — `key|RESERVED|<expiry-gate-or-phase>` — and `config_consumption.sh` FAILS if the
current gate/phase (`Docs/current-phase.txt`) is AT OR PAST that expiry with the marker still
present.**

- The two inherited module-2 `RESERVED` keys (`retry_count_4xx`,
  `variation_zero_width_rate_pct`) are grandfathered with named future expiries rather than
  exempted from the rule outright — the point is to close the gap going forward, not to pretend
  the inherited debt does not exist.
- The gate's own positive control is a past-expiry marker: `config_consumption.sh` must
  demonstrably go red on a key whose expiry has already passed, or the check itself would be
  exactly as vacuous as the absence it replaces (CLAUDE.md Rule 10's anti-vacuity standard, applied
  to a docs/config gate rather than a concurrency test).
- Removing a key once RESERVED means removing it everywhere in the same change — the seed row, the
  manifest, the reference doc, and the ledger entry together — not just deleting the marker and
  leaving an orphaned config row.

## Consequences

- A `RESERVED` marker is now a time-boxed escape hatch, structurally, not a permanent exemption an
  operator or a future reviewer has to remember to re-examine.
- `config_consumption.sh` §"RESERVED-marker expiry" section is the enforcement point; its own
  header comment cites this ADR and the two inherited defects that motivated it.
- No schema change and no new config key: this ADR governs a CI gate and a docs-ledger convention,
  not runtime behavior.
- **This ADR was written 2026-08-02, two days after the ruling it records (2026-07-31) and after
  the mechanism itself had already been implemented and was already enforcing the invariant in
  CI.** Found missing by Phase 6's mechanical landing check (M3-P2-OD-8's own ruling text required
  "an ADR appending the invariant," and the code/gate half landed while the ADR half did not) — the
  mechanism was never at risk, only its Rule-8 paper trail. Recorded here as a backfill, not a
  design change.

## Links

- CLAUDE.md Rule 1 (config-not-code) and Rule 8 (newly locked invariants are appended by reviewed
  decision).
- CLAUDE.md Rule 10 (anti-vacuity), applied here to the gate's own past-expiry positive control.
- `Docs/phase-2-strip-shared-pool.md` §1.2.1 and §8.3 (M3-P2-OD-8's ruling text).
- `Docs/open-decisions.md` — M3-P2-OD-8.
- `tests/harness/checks/config_consumption.sh` (the enforcement point), `Docs/config-consumers.txt`
  (the ledger syntax).
