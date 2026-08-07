# Docs/ — living documentation

Everything in this directory tracks the **running system** and is kept honest by CI drift gates.
Provenance — the locked phase specs, the ADRs, the decision ledger, the build record — lives
in [`history/`](./history/README.md) and is not part of operating or extending the module.

Start at the [root README](../README.md) if you have not already.

## Operate

| Doc | What it answers |
|---|---|
| [`install-and-deploy.md`](./install-and-deploy.md) | How do I stand this up on a real server, from a bare VPS to a verified first send? How do I add a worker, redeploy, roll back? What is the safe migration/deploy ordering? |
| [`operator-runbook.md`](./operator-runbook.md) | Something is broken — what do I do? Diagnostic trees for the real failure modes. |
| [`go-live-checklist.md`](./go-live-checklist.md) | What must be confirmed before the first live deployment? |
| [`disaster-recovery.md`](./disaster-recovery.md) | KMS key backup and recovery, DB restore, credential/key rotation. **Read the key-backup section before going live** — losing the master key is unrecoverable, and module 3's operator cannot rotate it (no side channel to any user's plaintext). |
| [`monitoring.md`](./monitoring.md) | Where do I point uptime monitoring and the alert webhook? |
| [`capacity-planning.md`](./capacity-planning.md) | How many users and accounts do I need for N sends/day, and what binds first? |
| [`user-facing-copy.md`](./user-facing-copy.md) | Every string a user reads, and the evidence it is audited against. A user reads the sentence, not an error code — this is the contract that keeps those sentences honest. |
| [`send-byo-local-test-guide.md`](./send-byo-local-test-guide.md) | How do I verify the whole system on a dev machine before deploying? The 13-stage local gate, and the honest list of what local testing cannot prove. |
| [`smoke-test-real-provider.md`](./smoke-test-real-provider.md) | The first-contact-with-a-real-provider procedure. Written, deliberately not yet run. |

## Reference (the integrator/extender tier)

| Doc | What it answers | Gate |
|---|---|---|
| [`config-reference.md`](./config-reference.md) | All 85 config keys: meaning, `value_type`, `default_value`, which subsystem consumes it. | `config_drift`, `config_consumption` |
| [`api-contract.md`](./api-contract.md) | Every route on both auth domains: request/response shape, error envelopes, status codes. | `contract_drift` |
| [`architecture-overview.md`](./architecture-overview.md) | The system's shape — services, boundaries, send path, where each guarantee is enforced. | — |
| [`security-posture.md`](./security-posture.md) | What is protected, how, and the honest residual risks — written to two audiences (an operator's own risk, and a user trusting the operator with a third-party credential). The page to hand a security reviewer. | — |
| [`future-work.md`](./future-work.md) | What is deliberately **not** built, and why it was deferred. | — |
| [`advisory-locks.md`](./advisory-locks.md) | Every advisory-lock key in the repo + the reserved band. | — |
| [`load-test-results.md`](./load-test-results.md) | The measured single-Control throughput ceiling (committed artifact, not a dashboard). | — |
| [`user-copy-strings.txt`](./user-copy-strings.txt) | The machine-checked ledger backing `user-facing-copy.md`: every string, its surface, the evidence it claims. | (copy ledger gate) |
| `config-manifest.txt`, `config-consumers.txt` | Machine-readable inputs to the config gates. Not prose — do not hand-edit without re-running the gates. | both config gates |
| `send_byo_backend_architecture.svg`, `send_byo_job_lifecycle_states.svg`, `module-3-architecture.mermaid`, `module-3-account-states.mermaid` | The canonical diagrams. | — |

**Operating rules for changing the code** are in [`../CLAUDE.md`](../CLAUDE.md) — canonical, and
deliberately not mirrored here (OD-11).

## The rule that keeps these docs useful

The reference tier (`config-reference.md`, `api-contract.md`) is the **single source** for config
keys and endpoint shapes. Operational docs **link** to it and never restate it — a config default
copied into a runbook is a default that will silently go stale. If you are about to document a key's
value or a request body outside the reference tier, don't.

**Build status: Phase 8 (polish, docs, close) in progress.** Phases 0–7 are built and tested on real
infra; Phase 8 is this repo's own final phase — no new features, only documentation, an interface
inventory, a shared-code manifest reconciliation, and a re-measured throughput ceiling. The
open-decisions ledger is **empty — all 97 items ruled** ([`open-decisions.md`](./open-decisions.md);
the last two, M3-P8-OD-7's docs-reorganisation path collision and M3-P8-OD-8's three expired
`RESERVED` config keys, were ruled 2026-08-04) and deferred work is registered in
[`future-work.md`](./future-work.md). See
[`architecture-overview.md`](./architecture-overview.md)'s build-status section for the per-phase
verdict and the known operational limits, and [`go-live-checklist.md`](./go-live-checklist.md) before
deploying live. The build-by-build *evidence* behind each phase's verdict is provenance:
[`history/`](./history/README.md) — module 3's own eight phase specs, its build plan, and its ADRs
(`history/decisions/0013`–), plus module 2's carried-across provenance under `history/module-2/`.
