# Sender Module (Module 2)

Backend-only email-sending module of the platform. Users supply only a mail list + a
template; everything else is handled backend-side. No UI.

**This repository is at Phase 0 (groundwork).** It contains the scaffolded services, the
migration tooling + empty `sender` schema, the real-infrastructure test harness, secrets/
cert scaffolding, local dev, and docs discipline — **no product/feature behavior**. The
nine locked phase specs live under `Docs/` and drive Phases 1–9.

- **Operating rules:** [`CLAUDE.md`](./CLAUDE.md) — read first; non-negotiable build discipline.
- **Phase 0 spec:** [`Docs/phase-0-groundwork.md`](./Docs/phase-0-groundwork.md).
- **Definition of Done:** [`Docs/definition-of-done.md`](./Docs/definition-of-done.md).
- **Open Decisions:** [`Docs/open-decisions.md`](./Docs/open-decisions.md).

## Layout

```
CLAUDE.md                 operating rules (the /init anchor)
docker-compose.yml        canonical dev==CI stack (root)
services/brain/           Go orchestrator — the ONLY DB client & sole decryptor
services/box/             Node.js SMTP relay — NO DB, NO KMS, memory-only creds
db/migrations/            central, self-tracking SQL migrations (sender_NNNN_*)
tools/sender-migrate/     the thin migration runner (Go)
tools/                    gen-certs, provision-box, gen-admin-key
tests/harness/            real-infra behavioral test harness (real PG + Mailpit + procs)
Docs/                     living docs: config ref, api contract, ADRs, DoD, open-decisions
Docs/                     LOCKED phase specs (1–9, 9.5, contract completions, phase 0)
deploy/                   prod/override material + .env.example
```

## Quickstart (local dev == CI)

```bash
scripts/dev.sh certs      # generate dev CA + mTLS leaves + dev KMS key + operator key
scripts/dev.sh up         # postgres -> migrate -> brain -> box -> mailpit (healthchecked)
scripts/dev.sh test       # run the real-infra harness against the live stack
scripts/dev.sh reset      # tear down (-v) and bring back up clean
scripts/dev.sh down       # tear down
```

> `make` is not installed on the Windows dev host; `scripts/dev.sh` is the portable
> entrypoint and `Makefile` delegates to it (CI's Ubuntu image has `make`). See
> `Docs/open-decisions.md` OD-16.
