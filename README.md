# coldmail-send-byo — the bring-your-own-SMTP sender (module 3)

An email-sending service where users bring their own SMTP accounts. A platform hands it a mail list and a template; a user adds their own SMTP credential through
a self-serve account surface, and the module handles the rest — routing across that user's own accounts, pacing, retries, error classification, and pulling
unhealthy accounts out of rotation. Users control their own assets (limits, From address, their accounts); the operator controls the shared substrate (queue
depth, per-user suspension, pacing).

**Status (2026-11-09):** built and closed — phases 0–8 plus the fix-plan items and CI findings since; the decision ledger has **zero open items**; all gates are
green on the GitHub runner. What stands between this and a live deployment is owner action (real-provider smoke test, vendor paperwork, host provisioning), each
listed with its status in [`Docs/go-live-checklist.md`](./Docs/go-live-checklist.md). It is one of three module repositories (verifier, managed sender, this one)
pinned together by the parent repository, which also holds the platform contract.

## How it works

Two services and one database. The **Control** (Go) is the only thing that talks to Postgres and the only thing that can decrypt an SMTP credential; exactly
one is ever active, guarded by a Postgres advisory lock. It claims queued jobs, picks a healthy sending account, decrypts that account's credential, and pushes the
job over mutual TLS to a **sender worker** (Node.js) — a dumb SMTP relay that ships no database driver and no KMS SDK, holds the credential in memory only, zeroes
it on every exit from the send path, and reports the outcome back. Workers scale horizontally and are never publicly reachable. Everything tunable — 89 keys — is a
seeded row in `send_byo.config`, changeable at runtime with no redeploy; there are no hardcoded tuning constants. Full shape in
[`Docs/architecture.md`](./Docs/architecture.md).

## Quick start (local, nothing leaves your machine)

Needs Docker with Compose. From a clone:

```bash
bash scripts/dev.sh certs   # dev CA + mTLS leaves + dev KMS key + operator key -> writes .env
bash scripts/dev.sh up      # postgres -> migrate -> control -> worker -> smtp-sink (+ test DNS), blocks on healthchecks
bash scripts/dev.sh seed    # dev fixtures (system user + dev worker row)
bash scripts/dev.sh test    # the real-infra behavioral harness (should be all-green)
```

That gives you a working stack sending into [Mailpit](http://localhost:9025) (host port `9025`, overridable with `HOST_PORT_MAILPIT`), a local SMTP sink. The Control's
health and API port is `9080` on the host. `bash scripts/dev.sh help` lists the rest (`up2` for a second worker, `logs`, `ps`, `reset`, `down`); `make`
delegates to the same script where it is available.

A fresh install with zero users and zero accounts is a correct steady state, not a bug — it is waiting for a user to add their own account.

## Where to go next

The documentation is laid out identically in all three module repositories. [`Docs/README.md`](./Docs/README.md) is the full map; the short version:

| You want to | Read |
|---|---|
| **Install this on a real server** | [`Docs/install-and-deploy.md`](./Docs/install-and-deploy.md) |
| **Fix something that is broken right now** | [`Docs/operator-runbook.md`](./Docs/operator-runbook.md) |
| Go live for the first time | [`Docs/go-live-checklist.md`](./Docs/go-live-checklist.md) |
| Back up or rotate keys, recover from a disaster | [`Docs/operator-runbook.md#disaster-recovery-key-backup-and-rotation`](./Docs/operator-runbook.md#disaster-recovery-key-backup-and-rotation) |
| Wire up monitoring and alerting | [`Docs/operator-runbook.md#monitoring`](./Docs/operator-runbook.md#monitoring) |
| Size a deployment | [`Docs/operator-runbook.md#capacity-planning`](./Docs/operator-runbook.md#capacity-planning) and [`#load-test-results`](./Docs/operator-runbook.md#load-test-results) |
| Look up a config key or its default | [`Docs/config-reference.md`](./Docs/config-reference.md) |
| Call the API (platform or operator) | [`Docs/api-contract.md`](./Docs/api-contract.md) |
| Understand how the system is built | [`Docs/architecture.md`](./Docs/architecture.md) |
| Review the security posture and residual risks | [`Docs/security-posture.md`](./Docs/security-posture.md) |
| Send the first real message through a real provider | [`Docs/go-live-checklist.md#real-provider-smoke-test`](./Docs/go-live-checklist.md#real-provider-smoke-test) |
| Know what is deliberately NOT built | [`Docs/future-work.md`](./Docs/future-work.md) |
| See how every past decision was ruled | [`Docs/open-decisions.md`](./Docs/open-decisions.md) and [`Docs/adr/`](./Docs/adr/README.md) |
| **Change this code** | [`CLAUDE.md`](./CLAUDE.md) — non-negotiable build rules; read first |
| Know why something is the way it is | [`Docs/history/`](./Docs/history/README.md) — phase specs, build plan, provenance |

## Layout

```
CLAUDE.md                 operating rules — the build-discipline anchor
docker-compose.yml        the dev stack, identical to what CI runs (postgres, migrate, control, worker, worker2, smtp-sink, coredns, dns-blackhole)
scripts/dev.sh            the one entrypoint for local dev
services/control/         Go orchestrator — the ONLY DB client, the SOLE decryptor
services/worker/          Node.js SMTP relay — no DB, no KMS, memory-only credentials
db/migrations/            centrally-owned, self-tracking SQL (send_byo_NNNN_*.up/.down.sql; 54 up files, 0001–0053)
db/preflight/             platform-table existence checks, run before migrating
tools/                    send-byo-migrate, gen-certs, gen-secrets, own-secrets, provision-worker (worker + admin keys)
tests/harness/            real-infra behavioral harness (real Postgres, real worker, real SMTP)
tests/harness/checks/     the 13 CI gates and their 5 self-tests
deploy/                   fixtures the dev/CI stack mounts: dns-test/ (the CoreDNS zones the From-gate tests resolve against) and pg-init/ (platform-table stand-ins)
Docs/                     living documentation: the nine documents, adr/, and history/ (provenance)
```

## Tests and CI

Tests run against real infrastructure — real Postgres, a real Control↔worker mTLS channel, a real SMTP server, a real DNS server. There are no mock-the-database or
mock-the-worker tests; that is a hard rule, not a preference (CLAUDE.md Rule 3). `bash scripts/dev.sh test` runs the full suite (100 assertions on the last runner
build); the workflow is `.github/workflows/real-infra.yml`.

Thirteen gates run on every push, each catching a specific class of drift. Four of them (marked) have a paired self-test that proves the gate goes red when its property is broken on purpose:

| Gate | Fails when |
|---|---|
| `config_drift` | a seeded config key is missing from `config-reference.md`, or vice versa |
| `config_consumption` | a seeded key has no real consumer and is not marked `RESERVED` (self-tested) |
| `contract_drift` | a served route is undocumented in `api-contract.md`, or vice versa |
| `migration_lint` | the migration chain has a gap, a missing `.down.sql`, or a non-self-tracking file |
| `test_enumeration` | a test package or gate script exists that nothing runs (self-tested) |
| `image_pins` | any image the repo pulls is a floating tag or unpinned by digest (self-tested) |
| `dns_search_domain` | the From-gate's verdicts change under a `resolv.conf` search domain (self-tested) |
| `drain_grace` | the SIGTERM drain timeout is not comfortably below the container/systemd stop grace period |
| `postgres_durability` | the default dev Postgres is RAM-backed (the RAM one is opt-in) |
| `secret_ownership` | a dev key is readable by anyone but its container uid, or is not mode 600 |
| `no_secret_leak` | the worker registration token or its hash appears in service logs |
| `dsn_scrub_wiring` | a driver call site returns an error without scrubbing the DSN out of it |
| `user_output_scrub` | `jobs.last_smtp_response` gains a reader outside its single accessor, `jobs.last_error_message` gains a reader in `platformapi/`, or `workerproto.Report.SMTPResponse` gains an unledgered consumer |

(The fifth self-test, `go_suite_selftest`, proves the harness itself records a failing Go package as FAILED.) Run any of them directly: `bash tests/harness/checks/<name>.sh`.
