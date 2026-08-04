# Docs/history/ — provenance, not documentation

**Nobody should read this directory to understand the system.** Everything here is the record of
how the module was designed and built: the locked phase specs, the build diary, the flag-don't-fix
decision ledger, and the ADRs. It is kept because *why* a thing is the way it is matters when you
change it — but it is not the manual.

**If you are trying to do something, go to the living docs instead:**

| You want to | Read |
|---|---|
| Understand what this service is | `../../README.md` |
| Install / deploy it | `../install-and-deploy.md` |
| Fix something that is broken | `../operator-runbook.md` |
| Recover from a disaster, rotate keys | `../disaster-recovery.md` |
| Look up a config key | `../config-reference.md` |
| Look up an endpoint shape | `../api-contract.md` |
| Understand the system's shape | `../architecture-overview.md` |
| Review the security posture | `../security-posture.md` |
| Know what is deliberately NOT built | `../future-work.md` |
| Go live | `../go-live-checklist.md` |

## What is in here

**Note (2026-08-02, M3-P0-OD-9 landed):** this directory holds module 3's own provenance now.
Module 2's phase specs, ADRs 0000–0012, build plan, pre-live gates, and final revision report were
relocated to `module-2/` — see that subdirectory's own README for the full scope and rationale.
What is left at this top level is module 3's own material, plus the two module-2 files the ruling
kept here for path-stable citation (`open-decisions.md`, `phase-build-record.md` — module 2's own,
referenced by path from module 3's docs but not module 3's record; see `module-2/README.md`'s
explanation of why those two specifically were NOT carried into `module-2/`).

**Update (2026-08-03, Phase 8 §3 reorganisation):** module 3's own eight phase specs
(`phase-0-fork-and-baseline.md` through `phase-8-polish-docs-close.md`), `module-3-build-plan.md`,
`fork-point.md`, and `plan-revision-log.md` moved here from the living tier — they describe how the
module was built, not how to run it. **Module 3's own `Docs/open-decisions.md` (the living,
actively-growing ledger — do NOT confuse it with this directory's inherited `open-decisions.md`
below) has NOT yet moved**, pending [M3-P8-OD-7](../open-decisions.md#m3-p8-od-7--awaiting-ruling-docshistoryopen-decisionsmd-is-already-occupied-by-module-2s-own-ledger-phase-8s-spec-instructs-relocating-module-3s-ledger-to-that-same-path):
this exact path is already occupied by module 2's own ledger (see below), and the two cannot share a
filename without either overwriting one or renaming it — an open question, not yet ruled.

| File / dir | What it is | Consult it when |
|---|---|---|
| `module-2/` | **Module 2's carried-across provenance** — its ADRs (`decisions/0000`–`0012`) and the phase specs for subsystems module 3 inherited, plus its own `definition-of-done.md` and pre-build planning artifacts. Its own README explains scope and the double-stale-path hazard. **Not module 3's record.** | You hit an inherited-code comment citing an ADR number or a module-2 §-reference and need the source. |
| `phase-0-fork-and-baseline.md` … `phase-8-polish-docs-close.md` | **Module 3's own eight locked phase specs.** Read the phase's own doc for that phase's design, tests, and exit criteria — this is the provenance behind what the living docs currently claim. | You want to know why the code does something a certain way, or what a phase's own DoD/exit criteria required. |
| `module-3-build-plan.md` | **Module 3's own** pre-build plan — the brief every phase spec above was written against. | You want the original scope/sequencing intent before any phase's own execution details. |
| `fork-point.md` | The first commit after the fork, and Gate A/B's evidence that the rename touched identity only, never behavior. Never rewritten. | You need to know exactly what module 3 inherited, verbatim, at the moment of the fork. |
| `plan-revision-log.md` | Per-document record of what changed in the module 3 plan set during the 2026-07-31 open-decisions ruling pass, and where. | You want to check an application of a ruling rather than just trust it happened. |
| `decisions/NNNN-*.md` | **Module 3's own ADRs**, numbered from `0013` (module 2's ADRs 0000–0012 live in `module-2/decisions/`, not renumbered, not added to). Append-only; superseding writes a new ADR and flips the old one's status. | You want the Context/Decision/Consequences for a module-3-specific settled trade-off. |
| `definition-of-done.md` | **Module 3's own** nine-item DoD gate, vocabulary-adapted (`byosmtp.*`, module 3's own additive-only framing per CLAUDE.md Rule 5 / M3-P1-OD-3). Per-phase tick-offs are NOT centralized here — each phase spec records its own DoD status inline. Module 2's original copy (with its own tick-off history) is `module-2/definition-of-done.md`. | You are checking what the nine DoD items require, or which phase doc to consult for its own tick-off. |
| `open-decisions.md` | **Module 2's own** flag-don't-fix ledger, carried at this path (not moved into `module-2/`) because module 3's own inherited-code comments cite it directly by this exact path. Module 3's own, actively-growing ledger is `Docs/open-decisions.md` (top-level, living — still there as of this note, see the update above) — do not confuse the two. | An inherited-code comment or migration cites `Docs/history/open-decisions.md` and you need the source ruling. |
| `phase-build-record.md` | **Module 2's own** build diary, carried at this path for the same path-citation reason as `open-decisions.md` above. Not module 3's build diary. | You want to know which module-2 phase introduced something a comment cites this file for. |
| `mailflow-worker (Sender Inspiration)/` | A **reference codebase** (not part of this repo; gitignored, has its own nested `.git`). The content-variation and header-engineering implementations were adapted from it. | You are changing variation logic and want the original reference. |

## A note on paths inside module-2's carried-across files

`module-2/`'s files were written when everything in module 2 lived flat in its own `Docs/`, then
moved into module 2's own `Docs/history/`, then carried into module 3's `Docs/history/module-2/` —
two layers of stale path. **Deliberately left as-written** at both layers, for the same reason
module 2 itself gave: rewriting a build record so its paths match a later directory layout is how
provenance quietly becomes fiction. See `module-2/README.md` for the exact resolution rule.

`open-decisions.md` and `phase-build-record.md` at THIS top level are module 2's own files, carried
at their original relative path rather than into `module-2/`, specifically so that direct citations
in module 3's inherited code (`Docs/history/open-decisions.md P8-OD-1`, etc.) keep resolving without
a rewrite. Internal references inside those two files of the form `Docs/open-decisions.md` or
`Docs/phase-4-dispatcher-and-routing.md` are module 2's own historical-layer paths and were,
likewise, deliberately left as-written — prepend `module-2/` (phase specs, ADRs) or resolve against
`Codmailling 2` directly (module 2's own ledger/build-record self-references) as appropriate.

**The same rule extends to module 3's own eight phase specs, `module-3-build-plan.md`,
`fork-point.md`, and `plan-revision-log.md`, moved here at Phase 8 §3.** Every internal citation
written while those files still lived at the top of `Docs/` — `` `Docs/fork-point.md` ``,
`` `Docs/phase-4-from-gate.md` ``, and so on — is **left as-written**, for the identical reason: they
moved together, as one commit, so every such citation still resolves correctly once read as relative
to `Docs/history/` rather than `Docs/`. Do not "fix" these paths to read `Docs/history/phase-4-from-
gate.md` inside the historical files themselves; the fix belongs only in files that stayed in the
**living** tier and now point at a moved target (already done, in the same commit as the move).
