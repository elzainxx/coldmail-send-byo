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

| File / dir | What it is | Consult it when |
|---|---|---|
| `module-2/` | **Module 2's carried-across provenance** — its ADRs (`decisions/0000`–`0012`) and the phase specs for subsystems module 3 inherited, plus its own `definition-of-done.md` and pre-build planning artifacts. Its own README explains scope and the double-stale-path hazard. **Not module 3's record.** | You hit an inherited-code comment citing an ADR number or a module-2 §-reference and need the source. |
| `decisions/NNNN-*.md` | **Module 3's own ADRs**, numbered from `0013` (module 2's ADRs 0000–0012 live in `module-2/decisions/`, not renumbered, not added to). Append-only; superseding writes a new ADR and flips the old one's status. | You want the Context/Decision/Consequences for a module-3-specific settled trade-off. |
| `definition-of-done.md` | **Module 3's own** nine-item DoD gate, vocabulary-adapted (`byosmtp.*`, module 3's own additive-only framing per CLAUDE.md Rule 5 / M3-P1-OD-3). Per-phase tick-offs are NOT centralized here — each phase spec records its own DoD status inline. Module 2's original copy (with its own tick-off history) is `module-2/definition-of-done.md`. | You are checking what the nine DoD items require, or which phase doc to consult for its own tick-off. |
| `open-decisions.md` | **Module 2's own** flag-don't-fix ledger, carried at this path (not moved into `module-2/`) because module 3's own inherited-code comments cite it directly by this exact path. Module 3's own, actively-growing ledger is `Docs/open-decisions.md` (top-level, living) — do not confuse the two. | An inherited-code comment or migration cites `Docs/history/open-decisions.md` and you need the source ruling. |
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
