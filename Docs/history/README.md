# Docs/history/ — provenance, not documentation

**Nobody should read this directory to understand the system.** Everything here is the record of
how the module was designed and built: eleven locked phase specs, the build diary, the
flag-don't-fix decision ledger, and the ADRs. It is kept because *why* a thing is the way it is
matters when you change it — but it is not the manual.

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

| File / dir | What it is | Consult it when |
|---|---|---|
| `phase-0-groundwork.md` … `phase-9-polish-and-documentation.md` | The **locked phase specs** (11 files incl. `phase-9_5-…` and `phase-9-contract-and-doc-completions.md`). The design of record, written before the code. **Never edited during build work** — see CLAUDE.md. | You need the authoritative intent behind a behavior, or the §-reference a code comment cites. |
| `phase-build-record.md` | The **build diary** — what each phase actually landed, chunk by chunk, plus what each review gate found and how it was fixed. Split out of the living `architecture-overview.md` in Phase 10. | You want to know which phase introduced something, or what a review pass caught. |
| `open-decisions.md` | The **flag-don't-fix ledger**. Every contradiction, ambiguity, and locked-fact conflict found across all phases, with its ruling and disposition. CLAUDE.md Rule 2's artifact. | You hit something that looks wrong and want to know whether it was already ruled on. |
| `decisions/NNNN-*.md` | **ADRs** — the formal record for decisions that resolved an ambiguity or locked a trade-off. Append-only; superseding writes a new ADR and flips the old one's status. | You want the Context/Decision/Consequences for a specific settled trade-off. |
| `definition-of-done.md` | The **nine-item DoD gate** every phase was held to, with each phase's tick-off. | You are auditing whether a phase genuinely closed. |
| `pre-live-gates.md` | The original **G1–G4 pre-live gate** table (platform-coordination sign-offs). Superseded operationally by `../go-live-checklist.md`, which is the ordered version an operator actually works through. | You want the reasoning behind a go-live item, not the checklist form. |
| `sender-module-build-plan.md`, `final-revision-report.md` | The **pre-build planning artifacts** — the original phase decomposition and the pre-build revision review whose findings produced Phase 9.5. | Archaeology on why the phase set is shaped the way it is. |
| `phase1-drafts/` | Working drafts that fed the Phase 1 data-model spec. | Rarely. Superseded by `phase-1-data-model.md`. |
| `mailflow-worker (Sender Inspiration)/` | A **reference codebase** (not part of this repo; gitignored, has its own nested `.git`). The content-variation and header-engineering implementations were adapted from it. | You are changing variation logic and want the original reference. |

## A note on paths inside these files

These documents were written when everything lived flat in `Docs/`. Phase 10 moved the historical
material into `Docs/history/`, so internal references of the form `Docs/open-decisions.md` or
`Docs/phase-4-dispatcher-and-routing.md` now resolve to `Docs/history/…`.

**The historical text was deliberately left as-written.** Rewriting a build record so its paths
match a later directory layout is how provenance quietly becomes fiction — the record should say
what it said at the time. Living docs, code comments, and CI gates were updated; the archive was
not. If a path in here does not resolve, prepend `history/`.
