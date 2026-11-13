# What moved out of, and into, the living tier (2026-11-09)

`Docs/history/` is frozen: the files already here were not edited by this change, so a few of them still cite the old locations below. This note is the key.
It is a **new** file; nothing else in this directory was touched.

## Moved out of `history/` (still the same text; new location)

| Was | Is now | Why |
|---|---|---|
| `Docs/history/decisions/0013-….md` … `0018-….md` | `Docs/adr/ADR-0013-….md` … `ADR-0018-….md` | all three module repositories keep their numbered ADRs at `Docs/adr/` |

Frozen files that still say `decisions/NNNN-*.md` or `history/decisions/` (this directory's `README.md`, `phase-8-polish-docs-close.md`, …) mean `Docs/adr/ADR-NNNN-*.md`.

## Merged into a canonical living doc, then moved here as provenance

These were top-level living docs. Their substance now lives in the canonical docs named on the right (headings preserved so a reader of an old citation can find
the passage); the original files are kept here byte-for-byte as the pre-merge record.

| Was | Merged into |
|---|---|
| `Docs/advisory-locks.md` | `Docs/architecture.md` — "Advisory locks" |
| `Docs/shared-code-manifest.md` | `Docs/architecture.md` — "Relationship to module 2 (the shared-code manifest)" |
| `Docs/capacity-planning.md` | `Docs/operator-runbook.md` — "Capacity planning" |
| `Docs/disaster-recovery.md` | `Docs/operator-runbook.md` — "Disaster recovery" |
| `Docs/monitoring.md` | `Docs/operator-runbook.md` — "Monitoring" |
| `Docs/load-test-results.md` | `Docs/operator-runbook.md` — "Load-test results" |
| `Docs/smoke-test-real-provider.md` | `Docs/go-live-checklist.md` — "Real-provider smoke test" |
| `Docs/module-3-local-test-guide.md` | `Docs/install-and-deploy.md` — "Developing and testing locally" (Part 2) |
| `Docs/send-byo-local-test-guide.md` | `Docs/install-and-deploy.md` — "Developing and testing locally" (Appendix A: the detailed walkthrough; its `Stage N` numbering is kept because code comments cite it) |

Renamed in place (no merge): `Docs/architecture-overview.md` → `Docs/architecture.md`.
