# Docs/ — the map

Everything in this directory (except `history/`) describes the **running system** and is kept honest by CI drift gates. The layout is the same in all three
module repositories (`coldmail-verify`, `coldmail-send-managed`, `coldmail-send-byo`): the same nine documents, an `adr/` for numbered decisions, a `history/`
for provenance.

Start at the [root README](../README.md) if you have not already.

## The nine documents

| Document | What it answers | Gate that keeps it true |
|---|---|---|
| [`architecture.md`](./architecture.md) | How is it built — services, boundaries, the send path, where each guarantee is enforced, the advisory-lock registry, the relationship to module 2's shared code, build status. | — |
| [`install-and-deploy.md`](./install-and-deploy.md) | How do I stand this up on a real server, from a bare VPS to a verified first send? Add a worker, redeploy, roll back, migration ordering. Also: developing and testing locally, and the detailed 13-stage walkthrough (Appendix A). | — |
| [`operator-runbook.md`](./operator-runbook.md) | Something is broken — what do I do? Diagnostic trees, plus [monitoring](./operator-runbook.md#monitoring), [capacity planning](./operator-runbook.md#capacity-planning), [load-test results](./operator-runbook.md#load-test-results) and [disaster recovery / key backup](./operator-runbook.md#disaster-recovery-key-backup-and-rotation). **Read the key-backup section before going live** — losing the master key is unrecoverable. | — |
| [`config-reference.md`](./config-reference.md) | All 89 config keys: meaning, `value_type`, `default_value`, which subsystem consumes it; the ordering invariants; the deliberate hardcoded bounds. | `config_drift`, `config_consumption` |
| [`api-contract.md`](./api-contract.md) | Every route on both auth domains: request/response shape, error envelopes, status codes. | `contract_drift` |
| [`security-posture.md`](./security-posture.md) | What is protected, how, and the honest residual risks — written to two audiences (an operator's own risk, and a user trusting the operator with a third-party credential). The page to hand a security reviewer. | — (the properties it describes are gated separately: `no_secret_leak`, `dsn_scrub_wiring`, `user_output_scrub`, `secret_ownership`) |
| [`go-live-checklist.md`](./go-live-checklist.md) | What must be confirmed before the first live deployment, with the **status of each item** (✅ verified / ⏳ owner action / ➖ not applicable) and the real-provider smoke test. | — |
| [`future-work.md`](./future-work.md) | What is deliberately **not** built, and why. Every entry has a reason and a reopen trigger. | — (a `RESERVED` config key may not point here; `config_consumption` withdrew that form, see ADR-0014) |
| [`open-decisions.md`](./open-decisions.md) | This module's decision ledger — every flag-don't-fix question and how it was ruled. **Zero open.** | — |

## Around the nine

| Path | What it is |
|---|---|
| [`adr/`](./adr/README.md) | Numbered architecture decision records (0013 onward; module 2's 0000–0012 are provenance in `history/module-2/decisions/`). |
| [`history/`](./history/README.md) | Provenance: the locked phase specs, the build plan, the closed plans, module 2's carried-across record, and [`RELOCATED.md`](./history/RELOCATED.md) — the key to where every moved document went. Frozen: files here are not edited, only added to. |
| [`user-facing-copy.md`](./user-facing-copy.md) | The audit of every sentence a user reads (From gate, SMTP error framing, notification templates). Specific to this module — it is the only one with user-facing prose. Backed by [`user-copy-strings.txt`](./user-copy-strings.txt); the gate that would check that ledger is not built ([FW-M3-21](./future-work.md#fw-m3-21--the-user-copy-ledger-gate-m3-p8-od-3-ruled-gate-it-is-not-built)). |
| `config-manifest.txt`, `config-consumers.txt`, `user-output-readers.txt`, `current-phase.txt` | Machine-readable inputs to the gates (`config_drift`, `config_consumption`, `user_output_scrub`). Not prose — do not hand-edit without re-running the gate that reads them. |
| `send_byo_backend_architecture.svg`, `send_byo_job_lifecycle_states.svg`, `module-3-architecture.mermaid`, `module-3-account-states.mermaid` | The canonical diagrams. |

**Operating rules for changing the code** are in [`../CLAUDE.md`](../CLAUDE.md) — canonical, and deliberately not mirrored here.

## The rule that keeps these docs useful

The reference tier (`config-reference.md`, `api-contract.md`) is the **single source** for config keys and endpoint shapes. Operational docs **link** to it and
never restate it — a config default copied into a runbook is a default that will silently go stale. If you are about to document a key's value or a request body
outside the reference tier, don't.

## Status

Zero open decisions; all phases built and closed; every gate green on the GitHub runner at the last push. What remains is **owner action**, not engineering — it is
listed with its current status in [`go-live-checklist.md`](./go-live-checklist.md) and ordered, for the whole system, in the parent repository's `OWNER-ACTIONS.md`.
