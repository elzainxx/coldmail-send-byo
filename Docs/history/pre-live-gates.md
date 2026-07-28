# Pre-live gates — confirm before Phase 1 runs against the LIVE platform DB

These are **required sign-offs on GOING LIVE**, not on implementing Phase 1. Phase 1's schema
and behavior are built and proven in the real-infra harness (a self-owned Postgres with dev
fixtures) with none of these confirmed. They become mandatory only when the module first
points at the **real shared platform database**. Each is documentation of a setup/confirmation
step — no code is built now (flag, don't fix; CLAUDE.md Rule 2).

| Gate | Ruling | What to confirm before live | Owner |
|---|---|---|---|
| **G1 — migration admin credential (OD-1)** | ruled 2026-07-23 | The migration runner connects via `MIGRATE_DATABASE_URL` as an **admin/owner-capable role distinct from `sender_module`** (it must `CREATE ROLE`/`CREATE SCHEMA` and assign ownership to `sender_owner`). Confirm which real role that is and that its credential is sourced from the platform's secret store (never committed, never in migration SQL). `sender_module` itself is login-only and never runs DDL. | platform-db + ops |
| **G2 — advisory-lock band (OD-10)** | ruled 2026-07-23 | The platform's own migrator/advisory locks avoid the sender module's **reserved `9_000_000_000–9_999_999_999`** band (see `Docs/advisory-locks.md`). Only matters because both run against the same instance concurrently. | platform owner (you) |
| **G3 — platform-table column shapes (OD-8)** | ruled 2026-07-23 | The **actual** `public.users` and `public.job_items` COLUMN shapes match what Phase 1's FKs/reads assume (`public.users(id uuid pk, …)`, `public.job_items(id uuid pk, campaign_id, user_id, recipient_email, rendered_subject, rendered_body, reply_to, status, created_at)` — ADR-0007). The Phase 0 pre-flight (`db/preflight/check_platform_tables.sql`) checks table **existence** only, deliberately (a column-level check would break whenever the platform legitimately adds a column). Confirm the real columns against the platform schema before Phase 1 migrations run live. | platform-db |
| **G4 — KMS IAM separation (S15)** | standing | The prod KMS key policy grants **Decrypt to the Brain role only**; the platform and operators hold no Decrypt (the load-bearing sole-decryptor invariant, not code-checkable). Verified against the real cloud KMS key policy at deploy sign-off. | ops + security |

**Not a pre-live gate (recorded for completeness):**
- **OD-19** — host antivirus TLS-inspection breaks host→localhost mTLS probes on the Windows
  dev host. Environment quirk only; mTLS is validated in-network and CI is unaffected.
  **Not a module defect, no action.**

> These gates are also reflected in the Phase 0 Open Decisions ledger (`Docs/open-decisions.md`)
> and, for G4, in the security completions (S15). They do not block Phase 1 implementation in
> the harness; they block the first live run.
