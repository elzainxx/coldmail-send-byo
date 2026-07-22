# docs/ — living documentation

These documents track the running system and are kept honest by CI drift gates. The
**locked phase specs** (design of record) live under `../Docs/` and are not edited during
build work.

| Topic | Authoritative source | Living mirror here |
|---|---|---|
| Config keys / defaults | Phase 9 §4 (`../Docs/phase-9-polish-and-documentation.md`) | `config-reference.md` (CI-verified vs `sender.config`) |
| Public + operator API (Domains A/B) | Phase 9 §5 | `api-contract.md` (CI-verified vs Brain routes) |
| System shape / data flow | the two SVGs in `../Docs/` | `architecture-overview.md` |
| Operating rules | `../CLAUDE.md` (canonical, OD-11) | — |
| Definition of Done | this dir | `definition-of-done.md` |
| Settled decisions | `decisions/NNNN-*.md` (ADRs) | linked from specs |
| Unresolved contradictions | `open-decisions.md` | flag-don't-fix ledger |
| Advisory-lock key registry | `advisory-locks.md` | — |

Phase 0 status and scope: `../Docs/phase-0-groundwork.md`.
