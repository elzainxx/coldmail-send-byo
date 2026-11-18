# Architecture Decision Records — module 3 (`coldmail-send-byo`)

Numbered, append-only. A decision that is superseded gets a **new** ADR and the old one's status flips; nothing is renumbered or rewritten. The
flag-don't-fix ledger that precedes an ADR is [`../open-decisions.md`](../open-decisions.md) (`CLAUDE.md` Rule 2). Module 2's ADRs 0000–0012, which module 3
inherited, are provenance in [`../history/module-2/decisions/`](../history/module-2/decisions/) — they are not renumbered and not added to. Module 3's own
numbering therefore starts at **0013**.

(Until 2026-11-09 these six files lived in `Docs/history/decisions/` under bare names `0013-….md`; they were moved here and prefixed `ADR-` so all three
module repositories use the same layout and file naming. The numbers, titles and text are unchanged. See `../history/RELOCATED.md`.)

| ADR | Decision | Status |
|---|---|---|
| [ADR-0013](./ADR-0013-pacer-shrink-selects-by-value-not-position.md) | The pacer's ceiling-shrink normalization selects lanes to keep **by value**, not by slice position | accepted |
| [ADR-0014](./ADR-0014-reserved-config-marker-requires-a-mechanical-expiry.md) | A `RESERVED` config-consumption marker requires a mechanical, gate-checked expiry | accepted |
| [ADR-0015](./ADR-0015-contract-drift-gate-extends-to-the-platform-surface.md) | `contract_drift.sh` extends to enumerate the platform-surface account routes | accepted |
| [ADR-0016](./ADR-0016-quarantine-threshold-drop-narrowly-extends-the-fork-exemption.md) | Dropping `quarantine_threshold` narrowly extends M3-P1-OD-3's fork-time-only exemption | accepted |
| [ADR-0017](./ADR-0017-dispatch-kill-switch-becomes-a-graduated-rung-ladder.md) | The dispatch kill-switch becomes a graduated rung ladder; `dispatch_enabled` is composed into it | accepted |
| [ADR-0018](./ADR-0018-every-autonomous-mutator-gets-a-shadow-ladder.md) | Every autonomous mutator gets an `off`/`shadow`/`enforce` ladder seeded at `shadow` | accepted |
| [ADR-0019](./ADR-0019-the-mvcc-recheck-path-is-forced-by-a-test-only-rls-gate.md) | The consume's MVCC re-check path is forced by a test-only row-level-security gate, not left to volume | accepted |
| [ADR-0020](./ADR-0020-post-v1-cancel-flags-then-cancels-and-the-guard-lives-in-the-database.md) | `POST /v1/cancel` flags, then cancels, in two transactions; "an in-flight item never returns to the queue once cancelled" is enforced by a database guard | accepted |

**Decisions made after ADR-0018 that have no ADR of their own are recorded in [`../open-decisions.md`](../open-decisions.md) under their own ids** (`M3-CI-OD-1` … `M3-CI-OD-4`: rooted DNS names, SPF
CIDR lengths, macro-bearing SPF targets, every image pinned by digest). They were ruled and implemented without an ADR of their own because each is fully
specified, with its evidence and its controls, in its ledger entry; if one is ever superseded it gets an ADR (ADR-0020 onward) that points back at the entry.

**Next number: ADR-0021.**
