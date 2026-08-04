# Sender Module

An email-sending service where users bring their own SMTP accounts. A platform hands it a mail
list and a template; a user adds their own SMTP credential through a self-serve account surface,
and the module handles the rest — routing across that user's own accounts, pacing, retries, error
classification, and pulling unhealthy accounts out of rotation.

**Status: complete and sealed.** Eleven build phases are implemented, tested against real
infrastructure, and closed. Verdict is GO for a pilot. Nothing is marked open. Before a first live
deployment, work through [`Docs/go-live-checklist.md`](./Docs/go-live-checklist.md).

## How it works

Two services and one database. The **Brain** (Go) is the only thing that talks to Postgres and the
only thing that can decrypt an SMTP credential; exactly one is ever active, guarded by a Postgres
advisory lock. It claims queued jobs, picks a healthy sending account, decrypts that account's
credential, and pushes the job over mutual TLS to a **sender box** (Node.js) — a dumb SMTP relay
that ships no database driver and no KMS SDK, holds the credential in memory only, zeroes it on
every exit from the send path, and reports the outcome back. Boxes scale horizontally and are never publicly
reachable. Everything tunable — 53 keys — is a seeded row in `byosmtp.config`, changeable at
runtime with no redeploy; there are no hardcoded tuning constants. Full shape in
[`Docs/architecture-overview.md`](./Docs/architecture-overview.md).

## Run it locally

Needs Docker + Docker Compose. From a clone:

```bash
bash scripts/dev.sh certs   # dev CA + mTLS leaves + dev KMS key + operator key -> writes .env
bash scripts/dev.sh up      # postgres -> migrate -> brain -> box -> mailpit, blocks on healthchecks
bash scripts/dev.sh seed    # dev fixtures (system user + dev box row)
bash scripts/dev.sh test    # the real-infra behavioral harness (should be all-green)
```

That gives you a working stack sending into [Mailpit](http://localhost:8025) — a local SMTP sink,
so nothing leaves your machine. `bash scripts/dev.sh help` lists the rest (`logs`, `ps`, `reset`,
`down`). `make` delegates to the same script where it is available.

A fresh install with zero users and zero accounts is a correct steady state, not a bug — it is
waiting for a user to add their own account. See [`Docs/operator-runbook.md`](./Docs/operator-runbook.md).

## Where to go next

| You want to | Read |
|---|---|
| **Install this on a real server** | [`Docs/install-and-deploy.md`](./Docs/install-and-deploy.md) |
| **Fix something that is broken right now** | [`Docs/operator-runbook.md`](./Docs/operator-runbook.md) |
| Go live for the first time | [`Docs/go-live-checklist.md`](./Docs/go-live-checklist.md) |
| Recover from a disaster / rotate keys | [`Docs/disaster-recovery.md`](./Docs/disaster-recovery.md) |
| Look up a config key or its default | [`Docs/config-reference.md`](./Docs/config-reference.md) |
| Call the API (platform or operator) | [`Docs/api-contract.md`](./Docs/api-contract.md) |
| Understand how the system is built | [`Docs/architecture-overview.md`](./Docs/architecture-overview.md) |
| Review the security posture and residual risks | [`Docs/security-posture.md`](./Docs/security-posture.md) |
| Size a deployment | [`Docs/capacity-planning.md`](./Docs/capacity-planning.md) — and [`Docs/load-test-results.md`](./Docs/load-test-results.md) for the measured run behind it |
| Wire up monitoring and alerting | [`Docs/monitoring.md`](./Docs/monitoring.md) |
| Send the first real message through a real provider | [`Docs/smoke-test-real-provider.md`](./Docs/smoke-test-real-provider.md) |
| Know what is deliberately NOT built | [`Docs/future-work.md`](./Docs/future-work.md) |
| **Change this code** | [`CLAUDE.md`](./CLAUDE.md) — non-negotiable build rules; read first |
| Know why something is the way it is | [`Docs/history/`](./Docs/history/README.md) — specs, ADRs, decision ledger |

The full index, including the rule that keeps these docs from drifting, is
[`Docs/README.md`](./Docs/README.md).

## Layout

```
CLAUDE.md                 operating rules — the build-discipline anchor
docker-compose.yml        the dev stack, identical to what CI runs
scripts/dev.sh            the one entrypoint for local dev
services/brain/           Go orchestrator — the ONLY DB client, the SOLE decryptor
services/box/             Node.js SMTP relay — no DB, no KMS, memory-only credentials
db/migrations/            centrally-owned, self-tracking SQL (sender_NNNN_*.up/.down.sql)
db/preflight/             platform-table existence checks, run before migrating
tools/                    byosmtp-migrate, gen-certs, gen-secrets, provision-box (box + admin keys)
tests/harness/            real-infra behavioral harness (real Postgres, real box, real SMTP)
tests/harness/checks/     the 7 blocking CI gates
Docs/                     living documentation (operate + extend)
Docs/history/             provenance — phase specs, ADRs, decision ledger, build record
deploy/                   production override material
```

## Tests and CI

Tests run against real infrastructure — real Postgres, a real Brain↔box mTLS channel, a real SMTP
server. There are no mock-the-database or mock-the-box tests; that is a hard rule, not a
preference (CLAUDE.md Rule 3). `bash scripts/dev.sh test` runs the full suite.

Seven blocking CI gates run on every push, each catching a specific class of drift (M3-P0-OD-10:
the count was originally corrected from "Five" to "Six" — `drain_grace` was already running in CI,
`.github/workflows/real-infra.yml`, but never counted here; Phase 7 added the seventh,
`user_output_scrub`):

| Gate | Fails when |
|---|---|
| `config_drift` | a seeded config key is missing from `config-reference.md`, or vice versa |
| `contract_drift` | a served route is undocumented in `api-contract.md`, or vice versa |
| `migration_lint` | the migration chain has a gap, a missing `.down.sql`, or a non-self-tracking file |
| `config_consumption` | a seeded key has no real consumer and is not marked RESERVED |
| `drain_grace` | the SIGTERM drain timeout is not comfortably below the container/systemd stop grace period |
| `no_secret_leak` | the box registration token or its hash appears in service logs (the SMTP-credential canary scan lives in the send-path harness) |
| `user_output_scrub` | `jobs.last_smtp_response` gains a reader outside its single named accessor, `jobs.last_error_message` gains a reader in `platformapi/`, a notification payload reads the raw column instead of the accessor, or `boxproto.Report.SMTPResponse` gains an unledgered consumer (Phase 7 §1.6) |

Run any of them directly: `bash tests/harness/checks/<name>.sh`.
