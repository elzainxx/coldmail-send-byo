# User-Facing Copy Contract

Module 3's audience is a **user** who reads the sentence, not an integrator who reads `error.code`.
Module 2 has no equivalent of this document — its copy is two `fixedMessages` tables (five codes in
`platformapi`, nine in `adminapi`), every string a fixed caller-safe sentence, read by a platform
integrator and an operator who both read the code, not the prose. Module 3's From-gate copy, SMTP error
framing, and notification templates were each specified in separate phases against separate evidence
standards; this document consolidates them into one audited contract, per Phase 8 §2.

## 1. The standard

Ruled by Phase 4 (M3-P4-OD-1): the From-gate copy says **"we couldn't verify this will authenticate,"**
never **"this will fail."** The reason is a protocol fact, not a tone preference — DMARC passes on
SPF-aligned **OR** DKIM-aligned (RFC 7489 §4.2), so DNS inspection can never prove a failure, only fail
to find evidence of one leg. Generalised, this is the same rule CLAUDE.md Rule 5 already states for
outcomes ("never invent an outcome the worker did not report"):

> **Every user-facing string is audited against the evidence the module actually holds at the moment it
> renders.** A string that asserts more than the evidence supports is a defect, whatever its tone.

## 2. The audit — three surfaces

### 2.1 Surface 1 — the From gate

Source: `services/control/internal/fromgate/copy.go` (`RenderCopy` and its branches), plus the
indeterminate-outcome refusal strings in `platformapi/account_update.go` and
`platformapi/account_bulk_add.go` — two distinct strings, one per file. Every branch is exercised by
`fromgate/copy_test.go`'s banned-phrase test ("will fail" / "will be rejected" are asserted absent, and
the check is itself shown to fire on a seeded bad string).

| String (id in ledger) | Evidence claimed | Verdict |
|---|---|---|
| `fromgate.allow_empty` | SPF/self-hosted match confirmed | PASS — no copy at all; aligned needs no acknowledgment |
| `fromgate.indeterminate` | DNS lookup did not answer | PASS — states inability to check, not a finding |
| `fromgate.block_full` | p=reject + no SPF record + no DKIM at any probeable selector | PASS — the one case the decision table itself classifies as BLOCK; copy still hedges ("we couldn't verify," never "will fail") |
| `fromgate.warn_malformed_spf` | Observed SPF PermError (malformed/duplicate record) | PASS — describes an observed record defect, not a send outcome |
| `fromgate.warn_unmatched_provider` | smtp_host matched no provider table entry | PASS — states "couldn't identify," not "unauthorized" |
| `fromgate.warn_enforcing_policy` | p=reject/quarantine + SPF leg not satisfied + DKIM leg unresolved | PASS — explicitly notes DKIM "can also pass" and can't be checked pre-send; frames the risk as "more likely," never certain |
| `fromgate.warn_ordinary` | No enforcing DMARC policy + SPF doesn't authorize | PASS — "probably send... won't be authenticated" is consistent with no enforcement observed |
| `fromgate.account_update_indeterminate_refusal` | DNS lookup did not answer on a PATCH | PASS — "nothing changed — try again," not a claim about the new address |
| `fromgate.account_bulk_add_indeterminate_refusal` | DNS lookup did not answer on a bulk-add row | PASS — "press Test on this account to retry," not a claim about the new address |

**Verdict: 9/9 PASS.** The trap named in the brief — a resolver timeout misread as "we checked and found
nothing" — does not occur: every indeterminate-outcome string in the tree says the check could not run,
never that it ran and found an absence.

### 2.2 Surface 2 — SMTP error framing

Source: `services/control/internal/dispatch/verify.go`'s `externalCodeFor` (the verify-path frame) and
`platformapi`'s report/quote/test handlers, which pass `smtp_response`/`error_class` through **verbatim**
(`provider_text.go`, `handlers.go`, `campaign_report.go`) with no added prose. The provider's raw text is
never itself audited here — Rule 5's framing is "the provider writes this copy, not us"; only the frame
this codebase adds is in scope.

| String (id in ledger) | Evidence claimed | Verdict |
|---|---|---|
| `smtp_frame.verify_connect_failed` | worker's `ReasonConnectRefused` | PASS |
| `smtp_frame.verify_timeout` | worker's `ReasonTimeout` | PASS |
| `smtp_frame.verify_tls_unavailable` | worker's `ReasonStartTLSUnavail`; worker structurally aborts before AUTH (Rule 6) | PASS — "before sending your password" is a structural fact, not an inference |
| `smtp_frame.verify_tls_failed` | worker's `starttls_failed` | PASS — "often a certificate problem" is hedged (often, not always) |
| `smtp_frame.verify_auth_failed` | worker's `ReasonAuthFailed` | **PASS, borderline** — "The username or password was rejected" states what happened (AUTH failed) without naming a specific cause; it does not claim "your password is wrong" the way the brief's canonical bad example does, and does not rule out app-password/blocked-sign-in/IMAP-disabled causes by omission the way an explicit diagnosis would. No behavior change needed. |
| `smtp_frame.verify_protocol_error` | worker's `ehlo_failed` | PASS |
| `smtp_frame.verify_connection_lost` | worker's "connection_closed"/"write_failed", or used as the catch-all default for an unrecognized reason slug | PASS — the catch-all asserts only "connection lost," never a fabricated specific cause for an unmapped slug |
| `report.smtp_response_passthrough` | `jobs.last_smtp_response`, verbatim | PASS — no frame text added at all |
| `report.error_class_passthrough` | `jobs.last_error_class`, the enum's own text | PASS — structural classification, not prose |
| `account_test.verification_unavailable` | verifier absent or a plumbing error | PASS — states inability to check |

**Verdict: 10/10 PASS.** No frame in the tree characterizes the provider's text into a single diagnosis
the way the brief's canonical bad example ("your password is wrong" for a 535) would. The `report`/`quote`
paths add no interpretive prose around `smtp_response`/`error_class` at all — the "frame" is the JSON
field name, nothing more.

### 2.3 Surface 3 — notification templates

Source: `services/control/internal/dispatch/account_effect.go` (`EnqueueNotification` call for
`account_quarantined`) and `campaign_expiry.go` (the `campaign_expired` INSERT). **Correction to the
brief's own example set:** grepping the tree for `campaign_behind_projection` and
`all_accounts_unverified` as notification conditions returns no hits as notifications —
`all_accounts_unverified` exists only as a `quote` feasibility reason code
(`platformapi/quote_feasibility.go`), an unrelated surface, and `campaign_behind_projection` does not
exist anywhere in the codebase. Per this task's own instruction to audit what actually exists rather than
invent copy, the two conditions below are the complete wired set, confirmed by `grep -rn
"EnqueueNotification\|INSERT INTO send_byo.notifications" services/control --include=*.go` (excluding tests).

**A further structural finding, not a copy defect:** neither condition renders English prose today. Both
write only structured fields (`condition`, `severity`, `subject_id`, and a `reason`/`campaign_id` value)
to `send_byo.notifications`; no route in `platformapi` or `adminapi` renders that table into copy, and
`dispatch/notify.go`'s delivery loop only advances `state` (`pending`→`delivered`/`suppressed`) — it
sends nothing out-of-band. There is therefore no rendered *sentence* to audit yet on this surface; the
table below audits the structured **claim** each payload's `reason` value carries, since that is what a
future render must stay honest to.

> **Amended 2026-08-03 (defect #134), and it matters for a future render's honesty.** The sentence above
> originally read "no route in `platformapi` or `adminapi` **reads** that table." Four routes now
> WRITE it — the two un-quarantine routes and the `auth_failure` auto-clear on both credential PATCHes
> stamp `cleared_at` on the resolved condition's live row (`clearAccountQuarantineNotification`, one
> copy per package per M3-P8-OD-7). Still no route renders copy from it, so the audit verdict below is
> unchanged. What changed is a claim a future render would otherwise inherit as false: before this fix
> nothing ever cleared a row, so a resolved-then-recurring quarantine folded into the stale row and was
> withheld for up to `notification_dedup_window_sec` (86400s). Any rendered notification must not imply
> "we tell you when this happens" for a recurrence unless the clear is in place — it now is.

| String/claim (id in ledger) | Evidence claimed | Verdict |
|---|---|---|
| `notif.account_quarantined` | `quarantineDecision()`'s own reason (`auth_failure` / `consecutive_failures` / `failure_rate`), computed from this account's own observed data on this outcome | PASS — each reason value is grounded in a real per-account signal (this attempt's AUTH result, this account's own streak counter, or this account's own windowed rate), never a system-wide inference |
| `notif.campaign_expired` | `campaign_expiry_sec` elapsed with this campaign's own queued jobs not drained, re-asserted under the row's own lock at fire time | PASS — an observed elapsed-time fact against the firing campaign's own rows, not a stale or borrowed read |

**Verdict: 2/2 PASS on the structured claim.** No behavior-change finding is flagged to
`Docs/future-work.md` from this audit — there is no dishonest string to fix, because there is no
rendered string yet. (The absence of a rendered notification body is itself already tracked structurally
adjacent to this gap — see `Docs/future-work.md`'s `FW-M3-10` on the delivery loop's missing health
signal — but building notification *rendering* is a design decision outside this Phase 8 polish task's
"flag, don't build" scope, and per the brief only a *dishonest existing string* triggers a future-work
entry here.)

### 2.4 Surface 4 — the reason text of account events (found by the gate, 2026-11-14)

The ledger gate (§4) scans every file that renders copy for prose that has no ledger row. Its first run
found two. `platformapi/account_events.go` returns `account_events.reason` to the user verbatim, and the
credential and alignment-acknowledging `PATCH` handlers (`platformapi/account_update.go` and its
break-glass twin `adminapi/account_update.go`) write two fixed strings there. They belong to none of the
three surfaces audited in 2026-08, so they had no row and no verdict. Audited now, against the code that
writes them:

| String (id in ledger) | Evidence claimed | Verdict |
|---|---|---|
| `account_events.alignment_ack_reason` | the user acknowledged an alignment warning against the stored verdict without changing the address (`!fromAddressChanging && AcknowledgeAlignmentWarning`, write confirmed) | PASS — "acknowledged … (no address change)" says what the user did, and that nothing else did |
| `account_events.credential_clear_note` | a credential `PATCH` was applied to an account quarantined for `auth_failure`, and it ended un-quarantined (`cur.Status.Quarantined && !nowQuarantined`) | PASS — an observed state change on this account; it does not claim the new credential works |

**Verdict: 2/2 PASS.** No behavior change is needed. **This surface is new scope**, added because the
gate found it; whether an operator-written `reason` (the free text of an admin action) is also user-facing
copy is not decided here — it is not a fixed string, so the ledger cannot enumerate it.

## 3. Summary

| Surface | Strings audited | PASS | Flagged for future-work.md |
|---|---|---|---|
| From gate | 9 | 9 | 0 |
| SMTP error framing | 10 | 10 | 0 |
| Notification templates | 2 (structured claims; no rendered prose exists) | 2 | 0 |
| Account-event reason text (added 2026-11-14) | 2 | 2 | 0 |
| **Total** | **23** | **23** | **0** |

No string in the current tree asserts more than its surface's evidence supports, and no finding requires
a behavior change to become honest.

## 4. This is a point-in-time audit — the ledger is the enumeration gate

> **Status 2026-11-14: the gate described below is BUILT** (FW-M3-21, ruled by M3-P8-OD-3):
> `services/control/internal/copyledger` runs in the harness, and its paired control
> `tests/harness/checks/user_copy_ledger_selftest.sh` runs in CI. The ledger is a checked table now. How it works:
> every site that renders or passes through a user-facing string carries a `// copy-ledger: <id>` comment on or directly
> above its statement; the test requires the ids in code, in `Docs/user-copy-strings.txt` and in the tables of this
> document to be the same set, and requires every prose literal in a file that renders copy to carry one. **Limits:**
> the list of files that render copy is curated (`copySites` in the test) — a NEW file that renders copy must be added
> to it, and the gate cannot discover that on its own; and it checks that the ledger is complete and coherent, not that
> the evidence column is true.

This document reflects the tree at the time of writing. What keeps it honest going forward is
`Docs/user-copy-strings.txt`: every string audited above has a `string_id|surface|evidence_it_asserts`
row there, in the same flat, machine-checkable shape `Docs/user-output-readers.txt` and
`Docs/config-consumers.txt` already use for their own enumeration gates. The gate (built 2026-11-14, see the
status note above) enumerates every marked site and every prose literal in the files that render copy, three-way
set-equals that enumeration against the ledger's `string_id` column and this document's tables, and fails CI on
either direction of mismatch — a string added to code without a ledger row, or a ledger row with no surviving
string. What such a gate **cannot** check mechanically is whether the `evidence_it_asserts` column is *true* —
that stays a human judgment, re-run at each phase that touches these surfaces, the same way this document was
produced.
