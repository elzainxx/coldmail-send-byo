# ADR-0009: account_events append-only trigger must permit the FK ON DELETE SET NULL update

- **Status:** accepted
- **Date:** 2026-07-23

## Context
The locked Phase 2 spec (`Docs/phase-2-account-management.md` §12) gives the DDL for the new
`sender.account_events` audit sink. That DDL specifies **two** properties that, taken literally,
contradict each other:

1. `account_id UUID … REFERENCES sender.accounts(id) ON DELETE SET NULL` — so that a `deleted`
   audit row **survives** the account's hard delete (§2.5, §7.4, test T9: "the `action='deleted'`
   row exists with `account_id` nulled by the FK … and the account's earlier lifecycle events
   also survive with nulled `account_id`").
2. A `BEFORE UPDATE` reject-trigger (`fn_account_events_reject_update`) that raises on **every**
   update, "same discipline as `job_events`."

Postgres implements `ON DELETE SET NULL` by issuing an internal
`UPDATE ONLY sender.account_events SET account_id = NULL WHERE account_id = <deleted id>`. A
row-level `BEFORE UPDATE` trigger fires on that maintenance update. So with the trigger as
literally written, deleting an account that has any audit rows **raises**
`integrity_constraint_violation` and the whole delete fails — making the `DELETE /v1/admin/
accounts/{id}` → `204` + surviving-audit behavior that §2.5/§7.4/T9 mandate **impossible**.

This was caught by the behavioral suite before merge: T5 (delete a pool after deleting its
account) and T9 (delete the account through the API) both returned `500 internal_error`, with the
Postgres log showing the reject-trigger firing on the FK's `SET NULL` update.

The spec **already cites the correct principle** — the Phase 1 `#23` lesson, quoted in §7.4 and
in the `0014` DDL comment: *"No `BEFORE DELETE` reject-trigger — it would block the FK
`SET NULL`/cascade cleanup."* The authors correctly removed the DELETE trigger but did not notice
that for `account_events` the **inbound** FK maintenance is a `SET NULL` (an UPDATE), which the
UPDATE trigger blocks for exactly the same reason. The spec is internally inconsistent, not
merely under-specified.

Per CLAUDE.md Rule 2 this is a locked-fact conflict: it was **flagged** (OD-23) and resolved on
the spec's own terms rather than silently patched or resolved by weakening either property.

## Decision (RULED — the only reading consistent with §7.4 + Phase 1 #23)
Make the `BEFORE UPDATE` reject-trigger **FK-aware**: permit **only** the FK-maintenance
transition and reject every genuine content mutation.

Concretely, the trigger returns `NEW` (allows the update) iff **all** of:
- `OLD.account_id IS NOT NULL AND NEW.account_id IS NULL` (the `SET NULL` transition), **and**
- every other column is unchanged (`id`, `user_id`, `actor`, `action`, `created_at` equal;
  `from_flags`, `to_flags`, `reason`, `request_id` `IS NOT DISTINCT FROM` their old values).

Any other update — a content change, or a content change bundled with `account_id → NULL` —
raises `integrity_constraint_violation` exactly as before.

This preserves the two things the append-only guarantee is actually protecting:
- **Content immutability** — you can never change *what happened* (action, actor, flag
  snapshots, reason, timestamps). That is the audit property that matters.
- **Defense-in-depth** — the trigger still backstops the `REVOKE UPDATE` grant against a
  future misgrant, for every mutation except the one the database itself must perform.

And it makes the mandated behavior work: the account delete's `SET NULL` succeeds, so the
`deleted` audit row (and all prior events for that account) survive with `account_id` nulled.

## Alternatives rejected
- **Drop the reject-trigger, rely on `REVOKE UPDATE` alone.** Loses the defense-in-depth
  backstop the spec explicitly wanted ("same discipline as `job_events`"). The trigger is the
  belt to the grant's braces.
- **Change the FK to `ON DELETE CASCADE` or `NO ACTION`.** Would either destroy the audit trail
  on delete (contradicts §7.4 "the audit survives") or block the delete (contradicts §2.5
  "returns `204`"). Both violate a locked behavior; the FK shape is locked as `SET NULL`.
- **Forbid deleting an account that has audit rows.** Contradicts §2.5 (delete is unconditional;
  no FK to `accounts.id` uses `RESTRICT`) and defeats the GDPR-style hard-delete the spec
  provides.

## Consequences
- `db/migrations/sender_0014_create_account_events.up.sql` implements the FK-aware trigger; the
  `down` is unchanged (drops the function/trigger/table).
- Proven on real Postgres by the behavioral suite: **T5** (delete pool after account delete →
  `204`) and **T9** (delete account through the API → `deleted` audit row survives with
  `account_id` nulled; earlier events survive nulled) both pass. **T9 also proves the trigger
  still has teeth**: a superuser content `UPDATE` and an `account_id=NULL` bundled with a content
  change are both rejected.
- No Phase 1–9 spec file is edited; the resolution lives in the migration + this ADR +
  `Docs/open-decisions.md` OD-23. The `job_events` trigger is unaffected (its inbound FK cleanup
  is a cascade DELETE, not a `SET NULL`, so its unconditional UPDATE-reject is already correct).

## Links
`Docs/phase-2-account-management.md` §2.5/§7.4/§12, ADR-0002 (Phase 1 #23 append-only ordering /
no-BEFORE-DELETE lesson), ADR-0008 (grants trap; account_events explicit grants),
`Docs/open-decisions.md` OD-23, CLAUDE.md Rules 2/5.
