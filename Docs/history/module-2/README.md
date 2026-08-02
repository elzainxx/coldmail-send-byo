# Docs/history/module-2/ — module 2 provenance; not module 3's record; paths inside predate two moves

This directory is **module 2's own historical record**, carried into module 3's repository by
**M3-P0-OD-9**'s ruling. It came from the sibling repository `Codmailling 2` (module 2, "sender"),
at the fork point recorded in `Docs/fork-point.md` (module 2 commit `21b65e6`, tagged
`module3-fork-point` in module 2's own repo). It is **not** module 3's build diary and must not be
read as one — module 3's own provenance is `Docs/history/`'s top level (its own phase specs, its
own ADRs starting at `0013`, and `Docs/open-decisions.md`), not this directory.

**Why it is here at all, per the ruling:** ADR-0008, ADR-0010, ADR-0011, and ADR-0012 are
load-bearing for code module 3 *inherited* — they are cited by number in inherited code comments
and by CLAUDE.md Rule 9 — so dropping them would orphan those citations. Carrying module 2's entire
`Docs/history/` (including its 156 KB decision ledger and 183 KB build record) would instead make
module 2's build diary look like it was module 3's own, which it is not. This directory is the
narrow middle: the ADRs and the phase specs for inherited subsystems, and nothing else. Module 2's
own decision ledger (`open-decisions.md`) and build record (`phase-build-record.md`) were
deliberately **not** carried; where module 3's docs need to cite either, they cite it by its
module-2 path (e.g. `Codmailling 2/Docs/history/open-decisions.md`), not a local copy.

## What is in here

| File / dir | What it is |
|---|---|
| `phase-0-groundwork.md` … `phase-9_5-operability-and-control-plane-safety.md` | Module 2's 12 locked phase specs (11 phases; Phase 9 has two files, Phase 10 has none — see module 2's own `README.md` history for why, restated verbatim below). |
| `decisions/0000-*.md` … `decisions/0012-*.md` | Module 2's 13 ADRs, unchanged. Module 3's own ADRs are numbered from `0013` in `Docs/history/decisions/` (module 3's own directory, one level up), so there is no collision. |
| `definition-of-done.md` | Module 2's own nine-item Definition of Done, exactly as module 2 left it (vocabulary: `sender.*`, the seven locked enums, the `sender_0001..0020` chain), kept here as historical reference. **This is not the DoD CLAUDE.md points at.** Module 3's own operating DoD — adapted vocabulary, no fabricated tick-off history — is `Docs/history/definition-of-done.md` (module 3's own directory), which is what CLAUDE.md's "Definition of Done" section cites. |
| `sender-module-build-plan.md`, `final-revision-report.md` | Module 2's pre-build planning artifacts (original phase decomposition; the pre-build revision review that produced its Phase 9.5). |
| `pre-live-gates.md` | Module 2's original G1–G4 pre-live gate table. Not module 3's — module 3's own pre-live gates are `Docs/go-live-checklist.md`. |
| `phase1-drafts/` | Module 2's working drafts that fed its Phase 1 data-model spec. |

## A note on paths inside these files — the Inherited path rule

These documents were carried across **exactly as module 2 left them**. This repeats, deliberately,
module 2's own convention (`Docs/phase-8-polish-docs-close.md` §3.1, the "Inherited path rule"):
*"historical docs are left as written — not rewritten so their internal links match the new
layout,"* because *"rewriting a build record so its paths match a later directory layout is how
provenance quietly becomes fiction — the record should say what it said at the time."*

Concretely, that means these files carry **two** layers of stale path, not one:

1. Module 2's own historical layer — module 2's `Docs/history/README.md` already warned that these
   files were written when everything in module 2 lived flat in `Docs/`, before module 2's own
   Phase 10 moved historical material into `Docs/history/`; an internal reference of the form
   `Docs/open-decisions.md` inside these files means module 2's `Docs/history/open-decisions.md`.
2. The move into module 3 — a reference of the form `Docs/history/decisions/0010-...md` inside
   these files, or in module 3's own docs describing what these files say, means **this** directory
   (`Docs/history/module-2/decisions/0010-...md`), not module 3's own `Docs/history/decisions/`.

Neither layer was rewritten. If a path inside one of these files does not resolve, resolve it
against module 2's own repository (`Codmailling 2`) first (layer 1), then prepend
`Docs/history/module-2/` if the target is one of the files carried here (layer 2). This directory's
own existence is the second move; do not add a third by editing these files to match it.
