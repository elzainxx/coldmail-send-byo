# Phase 8 — Polish, documentation, close

**Status:** PLAN ONLY. No code, no migration, no config row, no doc file is written by this document.
**Depends on:** Phases 0–7, closed. **Migration number:** none. Phase 8 writes no DDL; if it discovers
schema it wants, that is a future-work entry (§4), not a migration.
**Inherited without re-derivation, per the brief:** module 2's Phase 10 shape (six chunks in
`Docs/history/phase-build-record.md` — *there is no locked Phase 10 spec*, §0.2), its
`go-live-checklist.md` structure, its living/history split, and Phase 0 §5's shared-code manifest.
This document does not restate any of the four; it states where module 3's story diverges and why.

**The one rule that governs this whole phase:** *no new features.* Every section below can turn up a
capability that is missing. Each one goes into the future-work register (§4) with a stated trigger, and
**not** into a Phase 8 build. The audit is the deliverable; the fixes are the next project's.

---

## 0. WHAT THIS PHASE IS, AND THE TWO THINGS THAT MAKE IT DIFFERENT FROM MODULE 2'S

Module 2's Phase 10 produced the operational doc tier for **one operator running their own sending
infrastructure**. Module 3 produces the operational doc tier for **one operator holding many users'
third-party secrets**. The install steps are nearly identical. The security posture is not the same
document with the names changed, and §1.5 is where that is argued rather than asserted.

Two structural facts shape everything below.

### 0.1 The inversion's final form: the honesty of the residual-risk page

Phases 0–7 tracked the inversion through code. It appears one last time here, and this time it is not
a code property at all — it is **who the risk page is addressed to.**

Module 2's `security-posture.md` §3 lists sixteen residual risks. Every one is phrased as *what the
deploying operator needs to know and accept*, because in module 2 the operator **owns the credentials
being protected**. The page is handed to a reviewer who is signing off on their own deployment's risk.
That is a coherent document and a good one.

Module 3's operator does not own the credentials. A user's Google Workspace app password sits in
`byosmtp.accounts`, encrypted, and the operator's Brain host holds the key that decrypts it. So the
residual-risk page now has **two audiences with different interests**, and a risk that is acceptable
to one is not automatically acceptable to the other:

| | Module 2 | Module 3 |
|---|---|---|
| Who owns the stored credential | the operator (or their direct customer) | **the end user**, who has no operational relationship with the Brain host |
| Who the risk page addresses | the operator, accepting their own risk | the operator **and** the user, whose risk the operator is accepting **on their behalf** |
| What "we cannot read credentials" means | *can the deployer trust themselves* | *can the user trust the deployer* — a claim about a third party, made to someone who cannot verify it |
| The rotation fallback | "ask the account owner to retype it" — the operator IS the owner | there is **no** operator-side copy; the fallback is *ask every user to re-enter*, which is a support campaign, not a procedure |

This is not a reason to weaken the page. It is the reason §1.5 rewrites it rather than renaming it, and
the reason the residual risks the brief names are stated **as the brief states them** — a compromised
box exposes the credentials of accounts actively sending, break-glass operator read exists without
per-access user notification, `send_logs` holds users' bodies and recipients at rest, and
deleted-account credentials persist through the retention window. None of the four is new information.
All four were true in module 2 as well. What changed is that **the party who bears them is no longer
the party who accepted them.**

### 0.2 Module 2's Phase 10 has no locked spec, and this document does

Verified: `Docs/history/README.md` states *"Phase 10 has none — it was the close-out phase and its
scope arrived as an instruction rather than a locked spec, so its record is `phase-build-record.md`'s
'Phase 10 state' section."* Twelve spec files exist for eleven phases; Phase 10 contributes none.

So the "template to follow" is a **build record**, not a spec — six chunks written *after* the work,
plus `go-live-checklist.md` and the DoD write-up. Module 3's Phase 8 is being planned *before* the
work, like every other module 3 phase, which is a deliberate divergence and a mild improvement: the
audit questions get asked before the answers can be back-fitted to what was built. Module 2's own DoD
entry concedes the shape it had to use — DoD-1 for Phase 10 is justified as *"no spec file,
deliberately."* Module 3's Phase 8 does not need that move.

The six chunks are still the right *ordering*, because they are ordered by dependency: reorganise
first (§3) so the paths every other doc links to are final, then write the docs (§1), then verify by
reproduction (§5, §8), then close (§4). §6 and §7 are module 3 additions with no Phase 10 counterpart.

---

## 1. OPERATIONAL DOCS

Five documents. Four map onto module 2's Phase 10 chunks 2–4 and are largely a port; the fifth (§1.5)
is a rewrite.

**The standing rule, inherited and not restated per-document:** operational docs **link**, never
restate. Module 2's `Docs/README.md` says why — *"a config default copied into a runbook is a default
that will silently go stale."* The reference tier (`config-reference.md`, `api-contract.md`) is the
single source for every default and every endpoint shape. A Phase 8 doc that quotes a config default
inline is a defect, and §3.4's link sweep is where that gets caught.

### 1.1 `install-and-deploy.md` — single VPS, Brain + N box containers

Port module 2's structure (six parts: prerequisites → first-time setup in ten numbered steps →
env vars by service → production deployment → operating procedures → version skew). It is 964 lines of
command-by-command bash with prose after every block explaining *why*, and that density is the reason
the clean-slate reproduction (§8.1) works at all.

**What changes for module 3, and only these:**

| Step | Module 2 | Module 3 |
|---|---|---|
| Create a pool, add an account, set `default_shared_pool_id` (step 9) | required before anything sends | **deleted.** Phase 2 removes pools and the key. The operator provisions **no accounts at all** — users bring their own |
| Verify a real send end to end (step 10) | operator's own SMTP account against a real provider | needs a **user** with a verified account. The step becomes *create a test user, walk the Phase 3 add-account flow as that user, then send* — the first-run path is now a **user** journey, not an operator one |
| First-run empty state | "no accounts configured" is an operator misconfiguration | **a correct steady state.** A fresh module 3 install with zero users and zero accounts is not broken; it is waiting. §5.3 makes this a behavioral proof, not a doc claim |
| Prerequisites | Go toolchain, Docker, Postgres | unchanged, plus **outbound DNS egress** (Phase 4's From gate resolves TXT/MX from the Brain host) and **outbound SMTP to arbitrary hosts** — module 3's boxes connect to whatever server a user names, so an egress allow-list that worked for module 2's fixed provider set will silently break module 3 |

The last row is the one an operator will hit and not diagnose. It goes in prerequisites, where it is
read before the failure, not in the runbook where it is read after.

### 1.2 `operator-runbook.md` — the highest-value deliverable, and its triage tree inverts

Module 2's runbook is a symptom→steps decision tree, 959 lines, opening with a 30-second triage and
then branching per symptom. Keep the shape exactly.

**The content inverts, and this is the largest single divergence in §1.** Module 2's most common
incident is *"everything is queued and nothing is sending"* — eight numbered diagnostic steps, and
**six of the eight are operator-side causes** (dispatch off, shared pool unset, no boxes, no eligible
account, jobs not due, user out of quota). Module 3's most common incident is *"this one user's sends
are failing"* and the cause is **almost always outside the operator's control**: a wrong password, an
expired app password, a provider rate limit, a From address whose DNS never authenticated.

So the runbook needs a section module 2's does not have and cannot be ported from:

> **"A user says their sends are failing, and it is their server."**
> The operator's job here is **not to fix it** — they cannot; they hold no credential the user did not
> give them and no access to the user's provider. The job is to (a) read what the provider actually
> said, (b) decide whether the module misreported it, and (c) tell the user something true.

That section's steps are diagnostic-read-only, and every one of them terminates in *report to the
user*, not *change something*. It is also where Phase 7 §1's scrubbed `smtp_response` earns its keep:
the operator reads the same scrubbed text the user sees, which means **the operator cannot see more
than the user** on this path — worth stating explicitly, because an operator who assumes they have a
richer view will look for one and not find it.

Sections that port with content changes: `account_quarantined` triage (Phase 5's per-user thresholds,
not module 2's pool-wide score), `pool_exhausted` (**deleted** — no pools; replaced by *"a user has no
eligible accounts"*, Phase 7 §4.3's `no_eligible_accounts`), box death (unchanged), reading the health
endpoint (Phase 7 §7's per-destination throttle block is new), the Brain not running (unchanged),
mid-life singleton lock loss (unchanged, ADR-0011).

### 1.3 `disaster-recovery.md` — and the rotation section that has no fallback

Module 2 folds credential rotation into DR §3 rather than giving it its own file. **Keep that**, both
because it is the inherited shape and because rotation and recovery share the same premise: you need
the plaintext.

Five subsections port structurally. One of them **loses its escape hatch**, and it is the sharpest
operational finding in this phase:

> **Master-key rotation.** Module 2's procedure requires *"you must have every account's plaintext
> SMTP password"* — and module 2 notes even there that *"most originals are not written down
> anywhere else."* In module 3 that is not a weak assumption, it is a **false** one. The operator has
> never held the plaintext. There is no side channel. Re-encrypting every credential under a new master
> key requires **every user** to re-enter a secret the operator cannot supply for them.

State the consequence plainly in DR §3 rather than discovering it during an incident: **module 3's
master key cannot be rotated as an operator action.** It is a coordinated campaign against the whole
user base, with a re-add flow per user, and it takes as long as the least responsive user. That is not
a procedure this phase can write, and inventing one would be a new feature. It goes to future-work
(§4, `FW-M3-4`) with the honest note that the current answer is *"do not lose the master key,"* which
is a control, not a plan.

`§4a — suspected BOX compromise` ports with its trap intact (clearing `token_hash` only takes effect
at the next register handshake; kill the network first) and gains one step module 2 does not need:
**every user whose account sent through that box has had a third-party credential exposed, and telling
them is not optional.** Whether that notification is legally required is outside this document; that it
is *owed* is not.

### 1.4 `monitoring.md` + `capacity-planning.md`

Port. `monitoring.md` gains Phase 7 §7's throttle-pattern conditions and Phase 7 §3's notification
delivery health (a notification loop that has stopped delivering is invisible unless something watches
it — and module 2's poll-driven `fireWebhooks` precedent is exactly the failure mode Phase 7 §3.3
already rejected for users). `capacity-planning.md` is rewritten around §8's measured number, not
module 2's 370/min, and its account-count tables are per-user rather than per-pool.

### 1.5 `security-posture.md` — the page you would hand a reviewer, rewritten

**This is the one document in §1 that is not a port.** Module 2's §1 (assets) and §2 (controls →
mechanism → enforced by) tables port almost intact, because the *mechanisms* are custody-agnostic:
AES-256-GCM with AAD binding, Encrypt-in-admin/Decrypt-in-dispatch split, `credential_key_id` per-row
key labels, the two-vocabulary error rule, box-has-no-DB-and-no-KMS structural isolation. Every one of
those carries over unchanged and should be cited, not re-derived.

**§3 (residual risks) does not port.** Module 2's sixteen are each phrased as operator-accepts-own-risk
(§0.1). Module 3's §3 must be written to two audiences, and the four the brief names are the spine:

| # | Residual risk | Why it is honest to state it rather than claim mitigation |
|---|---|---|
| **R1** | **A compromised box exposes the credentials of accounts actively sending through it.** | Structural, not a gap. The box must hold the plaintext to authenticate to the user's server; memory-only + zero-on-every-exit-path bounds the window to *accounts sending during the compromise*, and bounding is all it can do. Module 2 states this too (§3.1) — what changes is that the exposed secret belongs to someone who never chose to trust this host. |
| **R2** | **A break-glass operator read exists, and the user is not notified per access.** | A `platform_admin` bearer key can drive any admin route. Module 2 called the analogous risk *"a Domain B operator can have any stored credential delivered in plaintext to a host they choose"* (§3.11) and treated it as an insider footnote. In module 3 it is the **primary** threat — platform-operator-as-adversary-to-user — and the absence of per-access user notification is a real gap, not a design choice. **Flag to future-work (`FW-M3-5`); do not build an audit-notification path in Phase 8.** |
| **R3** | **`send_logs` holds users' message bodies and recipient addresses at rest.** | TTL'd by `debug_log_retention_days`, but within the window it is plaintext campaign content belonging to users. Module 2 states this (§3.2) about its own customer's mail; module 3 states it about many unrelated parties' mail, which changes the blast radius of one DB compromise from *one tenant* to *all of them*. |
| **R4** | **A deleted account's credential persists through the retention window.** | Phase 3 §6's `account_retention_window_sec` (30d default) exists so a purge is auditable and reversible. The consequence is that *"I deleted my account"* and *"my password is no longer on your servers"* are **thirty days apart**, and a user reading a delete button does not assume that. State the window, state where it is configured, and state that setting it to zero is an operator choice with its own cost. |

Beyond the brief's four, port these module 2 risks with their framing corrected: KMS/IAM separation is
a **deployment** property not a code one (module 2 §3.3 — and in module 3 the honest sentence is
*"whoever has root on the Brain host can decrypt every user's credential"*, which is a much harder
sentence to hand a reviewer and is therefore exactly the one that must be there); losing the master key
is unrecoverable by design (§3.4); master-key rotation has no operator-side path (§1.3 above, and
strictly worse than module 2's §3.5); no rate limiting on the API surfaces (§3.7); a compromised Brain
host is total compromise (§3.8); certificate verification is skipped for private-IP SMTP hosts (§3.13
— and module 3 lets **users** name the host, so `isPrivateHost` is now user-reachable, which Phase 3
§2.2 already flagged as M3-P3-OD-13); a compromised box can fabricate the outcome of the job it holds
(§3.15); the alert webhook is unrestricted blind SSRF (§3.14).

**And one module 3 risk with no module 2 counterpart:** *the module authenticates to servers it does
not control, using mechanisms those servers choose.* Phase 7 §1.3's AUTH PLAIN finding is the concrete
instance — completeness of the credential scrub depends on a third party's configuration.

> **RULED 2026-07-31 (M3-P7-OD-2): (a) AUTH PLAIN IS IN SCOPE.** So this row is **bounded by test**,
> and the branch above is resolved rather than conditional. The reasoning recorded, because it is the
> whole shape of module 3: *"Users bring their own servers, including custom ones; refusing PLAIN
> breaks real users for a security convenience."* Refusing the mechanism was the cheaper engineering
> answer and it was rejected on product grounds.

The row therefore reads as bounded-by-test, and the test is nameable — Phase 7 §1.3's scrub covers
**all three base64 byte alignments plus the full PLAIN blob form, each with a paired control** (§10's
T1, five controls). Write it that way: *"the scrub is complete for LOGIN and PLAIN, verified by a test
per form; a server that invents a mechanism we do not speak is refused at negotiation, not scrubbed
after the fact."* That sentence is honest in both directions, which the conditional version could not
be.

**This stays a residual-risk row even though it is bounded by test**, and the reason matters: the bound
is over *mechanisms module 3 speaks*, not over *mechanisms a third-party server can propose*. The
reader needs to know who picks the mechanism — "we scrub the mechanisms we speak" is only reassuring
once that is said out loud.

**§5 — what a reviewer should verify at deploy time** ports as a `- [ ]` checkbox list. Module 2's
framing line stays verbatim in spirit: *"the codebase cannot check these; they are the deployment's
half of the posture."* Module 3 adds: KMS Decrypt is Brain-only **and no human role holds it**; the
`send_logs` retention window is set to a value the operator can defend to a user; the box egress
allow-list (if any) does not silently break user-named hosts.

---

## 2. THE USER-FACING COPY CONTRACT, CONSOLIDATED

One document, `Docs/user-facing-copy.md`, in the **reference** tier (§3) — it is a contract, and
operational docs link to it.

### 2.1 Why this needs a document at all, when module 2 has none

Module 2 has no copy contract, and correctly so: its copy is two `fixedMessages` tables (five codes in
`platformapi`, nine in `adminapi`), every string is a fixed caller-safe sentence, and the audience is a
platform integrator and an operator — both of whom read the `error.code`, not the sentence.

Module 3's audience is **a user**, who reads the sentence and not the code. Phases 3, 4, and 7 each
added a user-facing string, and they were specified in three separate documents against three separate
evidence standards. Consolidating them is not tidying; it is the only way to audit the standard.

### 2.2 The standard, and it comes from Phase 4's ruling

Phase 4 ruled that the From-gate copy must say **"we couldn't verify this will authenticate"** and
never **"this will fail."** The reason is a protocol fact, not a tone preference: DMARC passes on
SPF-aligned **OR** DKIM-aligned (RFC 7489 §4.2), so DNS inspection can never prove a failure — it can
only fail to find evidence of one leg. A string claiming failure would be **making a claim the module's
evidence does not support.**

Generalise it into the audit rule, because it is the same rule CLAUDE.md Rule 5 already states for
outcomes (*"never invent an outcome the box did not report"*):

> **Every user-facing string is audited against the evidence the module actually holds at the moment it
> renders.** A string that asserts more than the evidence supports is a defect, whatever its tone.

### 2.3 The audit — three surfaces, one table each

**Surface 1: the From gate (Phase 4 §5).** Block text, warning text, acknowledgment text. Audit each
against what the DNS check observed: *record absent*, *record present but unaligned*, *PermError on the
lookup limit*, *resolver unavailable*. The last is the trap — a resolver timeout is **not** evidence
about the user's DNS, and copy that conflates *we could not check* with *we checked and found nothing*
fails the standard. Phase 4's `from_dns_unavailable` reason exists precisely to keep those distinct;
the copy must too.

**Surface 2: SMTP error surfacing (Phase 7 §1).** The scrubbed provider response reaches the user
verbatim, which means **the provider writes this copy, not us.** Our copy is the frame around it. Audit
the frame: it must not characterise the provider's text (*"your password is wrong"* when the provider
said `535 5.7.8 Username and Password not accepted` is an interpretation, and providers return 535 for
reasons other than a wrong password — app-password requirements, blocked sign-in attempts, IMAP/SMTP
disabled at the workspace level). The honest frame is attribution: *your server said this*.

**Surface 3: notification templates (Phase 7 §3).** Nine-ish conditions × severity. Audit each against
the evidence its trigger holds: `account_quarantined` knows the failure pattern that tripped it and
should say so; `campaign_behind_projection` knows only that observed rate < projected rate, which is
**not** evidence of a problem (a provider being slow today is not a defect) and must not read as one;
`all_accounts_unverified` knows a verification state and nothing about whether sending would work.

### 2.4 The audit's own gate

A copy audit is a point-in-time check, and Phase 7 §1.6 established this repo's answer to that: a check
that must keep holding becomes a gate. Copy is harder to gate than a grep for a symbol — you cannot
mechanically assert a sentence is honest. What you *can* assert:

- **Every user-facing string lives in an enumerated table**, not inline at a call site — so a new one
  cannot be added without appearing in the ledger. Same shape as `fixedMessages`, and module 2 already
  proved it holds (`withMessage` overrides are all curated constants; the one interpolating site,
  `config_ordering_write.go:86`, interpolates a config key and value, not user input).
- **A ledger `Docs/user-copy-strings.txt`** of `string_id|surface|evidence_it_asserts`, three-way
  set-equal against the code's table — the `config_consumption.sh` shape, cloned a third time. What it
  cannot check is whether the evidence column is *true*; that stays human. What it **can** check is that
  no string exists without someone having written down what it claims.

**Flag, do not build:** if the audit finds a string that must change **behaviour** to become honest
(e.g. a message that requires evidence the module does not currently collect), that is future-work, per
the brief. Only the *string* changes in Phase 8.

---

## 3. DOCS REORGANISATION

### 3.1 The split, and it is inherited wholesale

Living = top level of `Docs/`. History = `Docs/history/`. Module 2's rule, verbatim in substance:
*"Everything in this directory tracks the running system and is kept honest by CI drift gates.
Provenance lives in `history/` and is not part of operating or extending the module."* And
`Docs/history/README.md`'s opening line, which is the whole discipline in eight words: *"Nobody should
read this directory to understand the system."*

**What moves to `Docs/history/` at Phase 8:** module 3's eight phase specs (`phase-0-fork-and-baseline.md`
through `phase-8-polish-docs-close.md` — including this file), `module-3-build-plan.md`, the module 3
ADRs, the module 3 decision ledger, and whatever module 2 provenance M3-P0-OD-9's ruling carried across
into `Docs/history/module-2/`.

**What stays living:** §1's five operational docs, §2's copy contract, `config-reference.md`,
`api-contract.md` (§6), `architecture-overview.md`, `future-work.md` (§4),
`shared-code-manifest.md` (§7), `load-test-results.md` (§8), `capacity-planning.md`, `monitoring.md`,
`go-live-checklist.md`, the two `.txt` machine-readable files, §2.4's copy ledger, and the `.mermaid`
diagrams.

**Inherited path rule, and it is load-bearing:** historical docs are **left as written** — not rewritten
so their internal links match the new layout. Module 2's reason is worth quoting because it is the
argument for the whole history tier: *"Rewriting a build record so its paths match a later directory
layout is how provenance quietly becomes fiction."* The rule for a stale internal reference is *prepend
`history/`*, stated once in `Docs/history/README.md`.

### 3.2 The hazard the brief names, and it is worse than "watch the gates"

The brief says *"Watch the CI drift gates — they reference doc paths."* Verified, and the precise shape
matters: **every gate hardcodes its doc paths as a bash string literal. There is no path-abstraction
layer.**

| Gate | Hardcoded path(s) | Failure mode if the path moves |
|---|---|---|
| `config_drift.sh` | `Docs/config-manifest.txt`, `Docs/config-reference.md` | greps an absent file → **every** manifest key reports as undocumented. Loud, at least |
| `config_consumption.sh` | `Docs/config-manifest.txt`, `Docs/config-consumers.txt`, `Docs/config-reference.md` | three `MANIFEST=`/`LEDGER=`/`REFDOC=` assignments; trivially repointed, and trivially forgotten |
| `contract_drift.sh` | `Docs/api-contract.md` | the final per-route `grep -q "$r"` loop finds nothing → every route reports undocumented |
| `drain_grace.sh` | `Docs/install-and-deploy.md` | scrapes `TimeoutStopSec=`; an absent file yields no match |
| `migration_lint.sh` | `tools/byosmtp-migrate/runner_integration_test.go` | not a `Docs/` path, but structurally the same literal-path coupling |
| §1.6's new user-output gate (Phase 7) | its own manifest + ledger | same shape, added by Phase 7 — reorganising after it lands means **six** gates to repoint, not five |

Two things follow.

**(a) The moves and the gate edits are one commit.** Module 2 hit exactly this and recorded it —
`config_drift` and `contract_drift` had to be updated in the same commit as the Chunk 1 move and re-run
green. Not "then fix the gates."

**(b) `Docs/README.md`'s doc↔gate table is prose, and nothing checks it.** Module 2's README documents
which gate maps to which doc (*"`config-reference.md` | … | `config_drift`, `config_consumption`"*),
and that table is the closest thing to a manifest of the coupling — and it is **not itself grepped by
any gate.** So the one artifact that would tell a future phase which gates to repoint can go stale
silently. That is a genuine gap, it is inherited, and Phase 8 should say so rather than repeat it.
Whether to close it (a seventh gate asserting each doc named in the README's table exists, and each
gate named is a real script) is small enough to be tempting and is still **new work** — register it as
`FW-M3-6` and let the ruling decide. Phase 8 must at minimum **update** the table in the same commit.

### 3.3 `Docs/README.md` — a real entry point

Port module 2's shape exactly: `## Operate` table (doc → what it answers), `## Reference (the
integrator/extender tier)` table (doc → what it answers → **Gate**), then the closing rule (reference
tier is the single source; operational docs link, never restate), then a build-status paragraph. Roughly
50 lines, pure index, zero procedural content — the discipline is that it holds *one line per doc*, so
a doc that cannot be described in one line is probably two docs.

Module 3's `## Operate` table has one row module 2's cannot: **§2's copy contract**, because in module 3
the words a user reads are an operational concern and not only a contract one.

### 3.4 The link sweep

Module 2 swept every `.md` link across all living docs mechanically, not by spot-check, and that is the
standard: after a reorganisation, a spot-checked link set is an unswept one. This is also where §1's
*link-don't-restate* rule gets verified — a doc that restates a config default has no link to sweep,
which is itself the signal.

---

## 4. FUTURE WORK REGISTER, AND THE LEDGER'S ACTUAL STATE

### 4.1 The merge, and a decision the brief leaves open

Two registers exist and are meant to become one. Module 3's own `future-work.md` says so, and names the
reason: *"At fork time the entries below are appended to that inherited file rather than kept as a
second register; two registers is exactly the staleness CLAUDE.md Rule 4 exists to prevent."*

**Check first whether Phase 0 already did it.** Module 3's file says the append happens *at fork time*,
which is Phase 0. Phase 0 is `PLAN ONLY` and unexecuted, so as of this writing the append has not
happened — but Phase 8 must **verify** rather than assume, because if Phase 0 executed it, Phase 8's job
is to audit the merged file, not to perform a second merge that duplicates every entry.

**The formats differ, and it is a real decision:**

| | Module 2 | Module 3 |
|---|---|---|
| Shape | 3-column table `\| Item \| What it is \| Why deferred \|` | labeled prose subsections under `## FW-M3-N` |
| Entry count | ~31 across 8 sections | **1** (`FW-M3-1`) |
| Per-entry fields | 3 | 7 — adds an **ID**, **Status**, **Trigger to revisit**, **Interaction to preserve** |
| Organised by | subject area (deliverability, routing, operator surface, retention, security, build infra, known defects) | one entry, no sections yet |

Module 3's format is strictly richer, and two of its extra fields are the ones that keep a register from
rotting: **Trigger to revisit** (without it, a deferred item is a TODO with better manners) and
**Interaction to preserve** (which is how a later build avoids re-introducing the thing the deferral was
protecting). Module 2's format is what ~31 existing entries are already written in.

**Recommendation, since the brief does not rule it:** keep **module 2's section structure** (its seven
subject areas are a good taxonomy and the inherited entries already sit in them), and adopt **module 3's
per-entry schema** for every entry module 3 adds or touches — as `### FW-M3-N` subsections *inside* the
relevant subject section, alongside the inherited table. Mixed, deliberately: back-filling seven fields
onto ~31 inherited entries is invented provenance (nobody recorded a revisit trigger for them, and
writing one now is fabrication), and flattening module 3's entries to three columns discards the two
fields that do the work. State the mixing rule in the file's header so it reads as a decision, not a
mess. **Registered as M3-P8-OD-1** — it is a docs-structure choice with a defensible default, so it
should not block, but it is the user's to overturn.

### 4.2 The entries Phase 8 adds, each traceable to a phase that flagged it

Not exhaustive — §1's, §2's, §5's, and §7's audits each generate their own — but these are already known
from phases 0–7 and should be in the register at Phase 8's open, not discovered at its close:

| ID | What | Flagged by |
|---|---|---|
| `FW-M3-1` | Ground-truth From verification via `Authentication-Results` | Phase 4 §1.6 / M3-P4-OD-1 (already registered) |
| ~~`FW-M3-2`~~ **CLOSED before Phase 8 opens** | Consolidate the eligibility predicate — was **six** readers | Phase 4 §8.1 / M3-P4-OD-3, **RULED (a) 2026-07-31: consolidated in Phase 4**, in its own commit. This is **not** future work: Phase 8's job is to **verify** the consolidation held (one definition + the named DDL copy, §6.3) and record it, not to register it as deferred. Phase 7 §4.3's `quote` **consults** the one definition (Phase 6 §5.5's rule) rather than becoming a sixth reader |
| `FW-M3-3` | Per-user notification preferences (a product feature, not a dial) | Phase 7 §8 / M3-P7-OD-7 |
| `FW-M3-4` | **Master-key rotation has no operator-side path in module 3** | §1.3 above. New here, and the most consequential item this phase generates |
| `FW-M3-5` | Per-access user notification for break-glass operator credential reads | §1.5 R2 |
| `FW-M3-6` | Machine-check the `Docs/README.md` doc↔gate table | §3.2(b) |
| `FW-M3-7` | Bounce / complaint ingestion | inherited from module 2 §1; still the largest absence, and module 3 does not close it |
| `FW-M3-8` | The two unpatched Go module vulnerabilities with live call paths | inherited from module 2's Phase 10 R3; module 3 forks the same `go.mod`, so it forks the same two. **Re-verify rather than inherit the claim** — the upstream fix may have landed since 2026-07-28 |

`FW-M3-8` is worth calling out as a class: module 2's register contains entries that are **facts about
a tree module 3 forked**, and some have expired. Phase 8 re-verifies inherited entries rather than
copying them forward, and marks each *confirmed*, *resolved upstream*, or *no longer applicable*.

### 4.3 "The ledger is TRULY empty" — the measured state, and it is not close

The brief asks that every deferred item move out of open-decisions *"so the ledger is TRULY empty across
phases 0-8."* Measured, so the target is a number rather than a hope:

**73 open decisions exist across phases 0–7. Nine are ruled. Sixty-four are not.**

Per-phase owned register rows: P0 = 11, P1 = 12, P2 = 9, P3 = 15, P4 = 12, P5 = 11, P6 = 5, P7 = 8.
Ruled: M3-P1-OD-1, -3, -4, -12; M3-P3-OD-3; M3-P4-OD-1, -9, -10; M3-P6-OD-1.

Three consequences the plan has to state rather than absorb.

**(a) ~~There is no `open-decisions.md` yet.~~ The ledger now EXISTS — this item is largely discharged
before Phase 8 opens.** Updated 2026-07-31.

When this was written, module 3's ledger lived only as a per-phase §-register inside each phase spec,
so *"move every deferred item out of open-decisions"* had no single file to move things out of, and
Phase 8's job was to **build** the consolidation. That work has since been done:
**[`Docs/open-decisions.md`](open-decisions.md)** is live, holds all **79** extracted decisions with
each one's phase, question and disposition, and every per-phase register points to it. This document's
own §9 and §12 already cite it as a live artifact — §4.3(a) was simply never reconciled.

**What is left for Phase 8 is therefore an audit and a RELOCATION, not a build:**

- The file sits at `Docs/open-decisions.md`, i.e. in the **living** docs. §3's reorganisation moves the
  phase specs to `history/`, and the ledger belongs with them — module 2's counterpart is at
  `Docs/history/open-decisions.md`. Move it and fix the inbound pointers (nine registers plus this doc).
- Re-audit the dispositions against the tree rather than accepting them, per Rule 11. The pass that
  built the ledger found a stale register row and six unmarked rows in one phase alone.
- **M3-P7-OD-9 was the one late-raised item, and it is RULED (b) 2026-07-31** — Phase 7's shape wins
  and `0033` is the single create site (Phase 7 §8, §13; Phase 1 §4.6). It **blocked the migration
  chain**, so Phase 8 does not audit it as a polish leftover but as a **structural precondition**: if
  the tree still has two create sites, the chain does not apply, no clean-slate run can be green, and
  item 2 of this same section fails. Verify it as applied — grep `0046` (renumbered from `0044` by
  M3-P5-OD-7's cascade, then from `0045` by M3-P6-OD-6's cascade) for `CREATE TABLE`/`CREATE TYPE`
  and expect nothing — rather than reading the
  register's ✅ (Rule 11).

**(b) 64 unruled decisions cannot be closed by Phase 8, and Phase 8 must not pretend otherwise.** Rule 2
is explicit: *"Never resolve a locked contradiction on your own authority."* An open decision is closed
by **the user's ruling**, not by the final phase's tidiness. So Phase 8's honest exit state is not "empty";
it is a ledger where **every entry is in exactly one of four dispositions**:

| Disposition | Meaning | Who closes it |
|---|---|---|
| **RULED** | the user ruled; the ruling is recorded with its rationale and the phase that implements it | already closed |
| **MOVED TO FUTURE-WORK** | not a contradiction — a deferred capability with a stated trigger | Phase 8 may do this itself |
| **OBSOLETED** | the question stopped existing (the code it asked about was deleted, or a later phase's design removed the choice) | Phase 8 may do this itself, **with the evidence** |
| **AWAITING RULING** | a genuine open question needing the user | **not closable by Phase 8.** Listed, with what is blocked |

The brief's own escape hatch — *"or explicitly listed as unclosable"* — is the right one, and on these
numbers it is the **main** path, not the exception. Phase 8's deliverable is the four-way partition and
an honest count, not an empty file.

**(c) Some are prerequisites for Phase 8's own sections, which makes them a sequencing problem.**
M3-P7-OD-2 (does module 3 speak AUTH PLAIN?) decides a §1.5 residual-risk row. M3-P7-OD-1 and -4 decide
what §6's inventory records as the error envelope and null-handling contract. M3-P4-OD-3 decides whether
§6 documents six eligibility sites or one. **Phase 8 cannot write those sections before those rulings**,
and should say which sections are blocked on which ruling rather than writing a plausible answer and
having it be wrong. Flagged as the sequencing note in §9, not as eight more open decisions.

> **RESOLVED 2026-07-31 — all four of those prerequisites are now ruled**, so this is no longer a
> sequencing risk for Phase 8, and the answers are fixed inputs rather than branches:
>
> | Prerequisite | Ruling | What Phase 8 writes |
> |---|---|---|
> | M3-P7-OD-2 | **(a) PLAIN in scope** | §1.5's row is **bounded by test** (§1.5 above) |
> | M3-P7-OD-1 | **(a) allow `smtp_response` + `error_class`**, conditional on OD-2 landing first | §6's inventory records the **widened** send-surface envelope |
> | M3-P7-OD-4 | as recommended | §6's null-handling contract as Phase 7 §1.1 specifies |
> | M3-P4-OD-3 | **(a) consolidate, own commit, first** | §6.3 documents **ONE** definition (§6.3 below) |
>
> The four-way partition in (a)/(b) above still stands as the method — this note removes the *blocked*
> class, not the classification work. Every module 3 OD is now ruled (`Docs/open-decisions.md`), so
> **`AWAITING RULING` should be empty when Phase 8 runs.** If Phase 8 finds a member of that class, it
> is a new decision surfaced during implementation, and it goes to the ledger rather than being
> absorbed.

---

## 5. HYGIENE AND LOG-NOISE SWEEP

Logging changes only. **No behaviour changes** — the brief says so, and the temptation here is real:
a log line that reveals a missing state transition is a *finding*, not a fix.

### 5.1 Is INFO useful during normal operation?

Measured on the inherited tree, so the sweep starts from a fact: **module 2's steady-state log stream is
two lines per loop iteration** — `"dispatch: tick pushed %d job(s)"` and `"reclaim: tick reclaimed %d
job(s)"` — plus per-admin-request lines and a periodic retention sweep line. Everything else is
error/anomaly path. With `dispatch_tick_interval_ms` at 1000, that is ~120 lines/minute of which the
overwhelming majority say **zero**.

Module 3 makes this worse in one specific way and better in none: Phase 6's per-user resolution and
Phase 7's notification loop each add a tick. So the question *"is INFO useful"* has a defensible answer
— **a tick that did nothing should not log** — and it is a logging change, in scope. The paired risk is
the one module 2's first-run empty state already surfaced: silence must be distinguishable from
*stopped*. Module 2's answer was a rate-limited `shared_pool_not_configured` line plus a health-endpoint
state; module 3's equivalent is §5.3.

Also in scope and worth doing while the code is fresh: **module 3 has no leveled logger.** The Brain is
100% bare `log.Printf`, and "levels" are a trailing `level=error` string token appended inconsistently
— present in `dispatch.go:129`, `runner.go:111`, `runner.go:186` and in `adminapi/logger.go`'s
deliberate `logReject`/`logError`/`logOK` convention, absent everywhere else (boxlink, main, singleton,
platformapi). Introducing a real logger is **not** a Phase 8 change (it touches every call site and is
a behaviour change in the "what gets emitted" sense). **Making the existing convention consistent is** —
every error-path line carries `level=error`, every rejection `level=warn`, and the sweep's deliverable is
that the token is applied uniformly rather than in four files out of eighteen. Register the real logger
as future-work.

### 5.2 Does an ERROR carry enough to act on?

The test is not *"is there a message"* — it is *"can an operator following the runbook (§1.2) act on
this line alone."* Concretely, per error site: does it name the **subject** (which user, which account,
which job), the **operation**, and the **decision the module made** (requeued / quarantined / dropped /
retrying)? A line reading `dispatch: tick error: <err>` names none of the three.

**The two-vocabulary rule cuts both ways here and is the thing to get right:** internal detail belongs
in logs *keyed by request_id* and never in a client envelope — so an ERROR line is exactly where the
internal reason string, the SQLSTATE, and the internal hostname are **supposed** to go. A sweep that
trims logs for tidiness can strip the only place that detail is allowed to exist. Trim volume, not
diagnosticity.

### 5.3 First-run empty state, as a behavioral proof

Module 2 proved this rather than documenting it: queued a real job with nothing configured, and confirmed
the system logged a rate-limited reason and showed `waiting_for_capacity` on the health endpoint rather
than going silent. Module 3's version differs because §1.1 established that a fresh install with zero
users is **correct**, not misconfigured:

- Brain up, zero users, zero accounts → health endpoint reports a *ready and idle* state, not an error,
  and the log does not repeat a complaint on every tick.
- One user, zero accounts, one queued campaign → `no_eligible_accounts` (Phase 7 §4.3), surfaced to
  **that user** (Phase 7 §3), not only to the operator's log.
- One user, one **unverified** account, one queued campaign → `all_accounts_unverified`, and the user
  is told what to do about it.

The third case is the module 3 one and the reason this is not a port: in module 2 an unverified account
is impossible (the operator verified it at creation); in module 3 it is the normal state of a user who
started an add-account flow and stopped halfway.

### 5.4 Standard hygiene

Zero `TODO`/`FIXME`/`XXX`/`HACK` across `services/`, `db/`, `tools/`, `tests/`, `scripts/`; `gofmt -l`
empty; `go vet` clean in all four modules; `go mod tidy` produces no diff; `npm audit` structurally
inapplicable while the box ships `dependencies: {}` (**verify that is still true** — Phase 3 through
Phase 7 all touch the box, and a single added dependency changes this from "inapplicable" to "unaudited").

---

## 6. THE INTERFACE INVENTORY

### 6.1 A conflict with the brief, flagged not resolved

The brief says: *"Match module 2's inventory format so the two are directly diffable."*

**Module 2 has no inventory document.** Verified: `grep -rliE "inventory" Docs/` over module 2 returns
**nothing**. There is exactly one artifact playing this role — `Docs/api-contract.md`, 438 lines — and
it fuses two concepts the brief treats as separate: it is simultaneously the *interface inventory* and
the *API contract*, and it is organised **by the phase that built each route** rather than by resource or
by auth domain.

Per Rule 2 this is flagged rather than silently resolved, and the resolution matters more than it looks,
because the brief's own stated purpose — *"the INPUT to the later all-modules comparison … assembling it
now beats archaeology"* — is defeated by either wrong answer. Write a new-format inventory and module 2's
side of the comparison stays un-inventoried, so the diff is against nothing. Copy module 2's
phase-grouped organisation literally and module 3's inventory is organised by **module 3's** phase
numbers, which do not correspond to module 2's — `## Phase 3 served routes` means the minimal send path
in one codebase and user-owned accounts in the other, so a section-by-section diff aligns headings that
share a name and nothing else.

**Recommendation, and it is the substantive call in this phase (M3-P8-OD-2):** adopt module 2's
*format* — every table shape, every column, every convention below — and change **only** the top-level
grouping, from by-phase to **by auth domain then by resource path**. Rationale: the format is what makes
a diff mechanical (same columns, same field-table shape, same error-vocabulary table, same terminal
boundary section), while the *grouping* is what makes it align. Phase grouping is provenance, and
provenance belongs in `history/`. Add a per-route **"Phase"** column so nothing is lost — the phase
that built a route is genuinely useful, just not as the organising axis.

That is a deviation from *"match module 2's format,"* so it is a decision for the user, with the default
stated and the cost of each alternative named above.

### 6.2 The format, captured — this is what "module 2's format" means concretely

Nine structural conventions, verified against `api-contract.md`. Reproduce all of them:

1. **Header states the authority chain** — *"living mirror of Phase N §M"* + the named CI gate that
   verifies it. Module 3's reads: mirror of the phase specs in `history/`, verified by `contract_drift`.
2. **Auth domains in prose bullets**, not a table, at the top — one bullet per domain, each naming the
   credential, the hashing/comparison method, and the **fail-closed mount condition**, plus an explicit
   *"Separation invariant"* bullet.
3. **The error envelope documented once**, as a literal fenced JSON example, with `details[].issue` as
   its own small table (`| The field was… | issue | Example |`).
4. **A master status/error table** — `| HTTP | error.code | When |` — one row per code, spanning every
   status the surface returns. This is the single most diffable artifact in the document.
5. **Route tables** — `| Method | Path | Purpose |`, with the auth stated **once in prose above the
   table**; a per-row `Auth` column appears **only** when one table mixes auth types (module 2's Phase 0
   table does, because healthz and the mTLS box link sit together).
6. **The `Purpose` cell absorbs prose** — request shape, idempotency rule, tenant rule, and the error
   condition all inline, as a dense paragraph, when the route's semantics do not fit a phrase. Module 2's
   `/v1/run` row is the anchor example and it is long on purpose.
7. **Request bodies get their own `### <VERB> <path>` sub-section** after the table, with a
   `| Field | Required | Type | Notes |` table. `Required` is bold `**yes**` vs plain `no` — not a
   boolean, not `Y/N`.
8. **Responses:** fenced ```jsonc with `//` inline comments for structured/nested shapes; a
   `| Field | Type | Notes |` table for flat ones.
9. **A terminal boundary section** — *"What is NOT part of the public REST contract"* — naming what the
   document deliberately excludes. Module 2 excludes the box↔Brain wire protocol and says why (internal
   machine-to-machine, evolves independently of additive-only REST versioning). Module 3's excludes the
   same, **plus** Phase 5's internal autonomous-subsystem behaviour.

Plus two conventions that are prose in module 2 and should stay prose: the **tenant-scoping callout**
(a blockquote before the route table — module 2's states that cross-tenant access returns `404
not_found`, byte-for-byte identical to the non-existent-id case except `request_id`, never `403`) and
**pagination**, documented inline at the route rather than in a global section.

### 6.3 What module 3's inventory records, and where it already diverges

Sixteen `/v1/*` routes are declared across module 3's phase specs. That is a floor, not the final set —
the inventory is written against **the served surface**, and `contract_drift` is what proves the two
agree. Known content:

- **Domain A (platform service token):** `quote`, `run`, `report`, `describe` — the core four, meant to
  be **uniform across modules**, and module 3 already diverges: Phase 7 §4.3 adds a **required
  `user_id`** to `quote`'s request (M3-P7-OD-5), and `quote` returns a feasibility verdict rather than a
  price, with `capacity`, `estimated_duration_sec` (required and non-null on every `feasible: true`),
  and `warnings[]`.
- **Domain B (operator `platform_admin` key):** the admin surface minus the five pool routes Phase 2
  deletes, plus Phase 6's per-user override routes (`PUT /v1/admin/users/{id}/config`,
  `DELETE /v1/admin/users/{id}/config/{dial}`), plus `GET /v1/admin/health` with Phase 7 §7's
  throttle-pattern block.
- **The user surface — `/v1/byosmtp/*` — which has no module 2 counterpart at all.** Account CRUD,
  bulk add, the test button, test-send. This is the inversion in the inventory: module 2's interface has
  two audiences (platform, operator) and module 3's has **three**, and the third one is the product.
  A comparison table with two columns cannot hold it; state that in the boundary section (§6.2 item 9)
  so the later all-modules diff does not read the extra surface as a mismatch.
- **The error envelope**, and here the inventory records a **contract divergence, not a bug**: Phase 3
  (M3-P3-OD-3) and Phase 7 (M3-P7-OD-1) widen the envelope with `smtp_response` and `error_class` on the
  send and verify surfaces. Rule 5's envelope is `{external code, message, request_id}`. The widening is
  the whole point of module 3's product, and recording it precisely — which surfaces, which fields, under
  which ruling — is the highest-value line in the inventory for the all-modules comparison, because it is
  the one place module 3 knowingly leaves the shared contract.
  **Both are RULED 2026-07-31 as allowed, with an ordering condition the inventory must record**: the
  widening ships only **after** M3-P7-OD-2's scrub work is complete (Phase 7 §12 criterion 6a). So the
  inventory entry carries not just *which fields on which surfaces* but *what makes them safe* — the
  five-control scrub — because a future reader who copies the widened envelope into a fourth module
  without the scrub inherits the field and not the guarantee. Phase 3's verify surface is covered
  a fortiori by the send-surface ruling; it is not a separate divergence.
- **The eligibility predicate: ONE definition, not six.** M3-P4-OD-3 is **RULED (a) 2026-07-31** —
  consolidated in Phase 4, in its own commit, before either new term. This resolves what §9's blockers
  table listed as an open input to this section. What the inventory records is therefore a **single**
  definition plus the terms module 3 added to it (`credential_verification_state` from Phase 3,
  `from_verification_state` from Phase 4) and the one copy that could not be absorbed —
  `accounts_eligibility_idx`'s `WHERE` clause, which lives in SQL DDL and is kept adjacent and
  cross-referenced instead (Phase 4 §8.1). Record the index deliberately: an inventory that says "one
  definition" without naming the DDL copy is the exact false comfort the consolidation was meant to end.
- **`error.code` vocabulary:** module 2's eight, plus module 3's additions across phases 4–7
  (`from_gate_blocked`, `no_eligible_accounts`, `all_accounts_unverified`, `exceeds_queue_cap`, and the
  rest). The master table (§6.2 item 4) is where these become diffable.
- **Status vocabulary:** `job_state` (unchanged, six values), `error_class` (six), `box_status` (three),
  plus module 3's account verification states from Phase 3 and `pause_reason` additions.
- **`describe`'s extension-route declaration** (Phase 7 §5) — the inventory records the declaration
  *format*, because that is the machine-readable half of this same surface, and a `describe` output that
  disagrees with the inventory is a drift the gate should catch.
- **Two known null-handling inconsistencies inherited from module 2** (M3-P7-OD-4: the single-job report
  **omits** `provider_message_id`/`completed_at` when nil; `campaignItem` emits explicit **null**).
  Recorded, **not fixed** — Phase 7 already ruled that a client distinguishing absent from null would
  break, and the inventory is exactly where the whole family gets reconciled at once, later.

### 6.4 The gate

`contract_drift.sh` ports with its route list updated and its **printed count corrected** — Phase 2
§1.3 found module 2's literal says *"22 routes gated"* while the array holds 21, and after Phase 2's
five deletions module 3's correct figure is **16**. That stale count is decoration no assertion checks;
the loop iterates the array, so the number cannot fail. Phase 8 should ask whether the count becomes an
**assertion** rather than a print — a one-line change, in scope for a docs/gate phase, and it converts a
decorative number into a checked one.

---

## 7. THE SHARED-CODE MANIFEST RECONCILIATION

### 7.1 What the manifest is, and the two things it is not

Phase 0 §5 established it, and its own framing is the scope: *"A bug found in either codebase can be
checked against the other, because the manifest says which files are still the same code. That is the
whole claim."* Two disclaimers carry forward verbatim and Phase 8 must not soften them: fixes flow
**module 3 → module 2 only, and rarely**, because module 2 is sealed; and *"this is a checking aid, not
a sync promise."* An "inherited verbatim" row is a claim about **the fork point**, never about today.

The verification is mechanical, and it only works because Phase 0 §1.2 requires a history-preserving
**clone**: `git merge-base module3-fork-point HEAD` confirms shared ancestry; `git diff
<fork-point>..HEAD -- <path>` empty means still verbatim, which means a module 2 bug applies here
unchanged. Without shared ancestry every row degrades to unverifiable prose.

### 7.2 A schema mismatch to fix while reconciling

Phase 0 §5.3 **states** a five-field schema — *"area · paths · inherited state at fork · divergence
expected at phase N · module-2 provenance reference"* — and the table it renders has **three columns**
(`| Area | Paths | Divergence expected |`), across 15 area rows. The two do not match. Phase 8 builds
the real `Docs/shared-code-manifest.md`, so Phase 8 is where that reconciles: either populate all five
fields or amend the stated schema to three. **Recommend five** — "inherited state at fork" is what makes
a row checkable, and "module-2 provenance reference" is what makes a found bug traceable to the ADR or
phase spec that explains the code. Both are cheap now and archaeology later.

### 7.3 What actually diverged — the axis Phase 0 could only predict

Phase 0's `Divergence expected` column was a forecast. Phase 8 replaces it with an observation, per area,
using the §7.1 git commands. The forecast's own extremes are where to look first: *"Box service — none
planned, inherited whole"* against *"Dispatcher, routing, eligibility — Phases 1, 2, 6, heavy."*

The box row is the interesting one, because it was forecast as **untouched** and phases 3, 4, and 7 all
have designs that reach into it (Phase 3's credential verification, Phase 4's From handling, Phase 7's
scrub coverage and the `SMTPError` constructor-order test). If the box is no longer verbatim, every
module 2 box bug stops applying unchanged — which is precisely the claim the manifest exists to support.
Check it explicitly rather than trusting the forecast.

**The forecast is already known to be wrong, and here is the row that proves it** — recorded now so
Phase 8 writes an observation it can defend rather than re-deriving one:

| Area | Forecast | Observed divergence | Driver |
|---|---|---|---|
| `smtp.js` — `scrubSecret` | none planned, inherited whole | **module 3's `scrubSecret` THROWS on a zeroed buffer; module 2's returns its input unchanged** (`catch { return text; }`) | **M3-P7-OD-8, RULED 2026-07-31: do both** — assert the ordering *and* fail closed |
| `smtp.js` — scrub coverage | none planned | module 3 redacts **all three base64 byte alignments plus the full PLAIN blob**; module 2 covers AUTH LOGIN only | **M3-P7-OD-2, RULED (a): PLAIN in scope** |
| `fn_job_events_on_state_change` (SQL, not JS) | *not forecast at all* — the manifest's axis is source files, and this is a **migration-resident function body** | module 3's writes one extra column (`destination_domain`, `split_part(recipient_email,'@',2)`); module 2's does not | **M3-P7-OD-6, RULED (a): denormalize** — the column has no other writer |

All three rows are **behaviour changes to shared code**, which is exactly the class the manifest exists
to record. The first is a divergence in *failure mode* rather than in feature set — the subtler and
more important kind. The third matters for a different reason: it is the first divergence in **shared
SQL**, and it exposes a limit in the manifest's own axis. A trigger function's live definition is not
in any one file — it is the last `CREATE OR REPLACE` in a migration chain (here `0022`, not the `0008`
that created it). So "is this file still identical?" cannot answer "is this function still identical?"
Phase 8 records that limitation rather than working around it: for SQL-resident logic, the manifest
entry must name the **defining migration**, not the table's creating migration. Write the entry so the direction is unmistakable: module 3 is **strictly more
fail-closed** on the credential path. The reasoning to carry over verbatim is *"fail-closed beats
fail-silent on a credential path"* — a scrub that runs after the zero returns raw provider text and
reports success in module 2, and crashes in module 3. Note the two safety properties that make the
crash acceptable rather than reckless, since a reviewer will ask: Rule 5 holds (the box reports **no**
outcome, so the Brain reclaims by lease expiry and never fabricates one) and Rule 6 holds (the zeroing
`finally` still runs on the throw path).

**And the consequence for the bug log below:** these two rows mean §7.4's entry 3 is no longer a
description of a shared latent bug. Module 3 has diverged; module 2 has not. That is a *decision to
port or not*, and it belongs in the all-modules pass as such — see §7.4 entry 3's note.

### 7.4 The module 2 bug log — concrete entries for the all-modules polish pass

The brief names four. All four are real and traceable to the phase that found them:

| # | The bug, in module 2's tree | Found by | Status in module 2 |
|---|---|---|---|
| **1** | `contract_drift.sh:69` prints *"22 routes gated"*; the `ADMIN_ROUTES` array holds **21** and `server.go:45-73` mounts 21. The count is decoration no assertion checks | Phase 2 §1.3 | present, unfixed |
| **2** | `pacer.normalize`'s shrink comment is **aspirational, not descriptive** — it states "drop the most-ready lanes" and the code does a positional `slots[len-ceiling:]` front-drop, which forgives cooldown a lane still owes | Phase 6 §3.3 / M3-P6-OD-1, ruled 2026-07-30 | present, unfixed. Module 3 fixes it **as a bug fix**, and the ADR records why it is not a behaviour change |
| **3** | The credential scrub is complete **only for AUTH LOGIN**. AUTH PLAIN's base64 blob defeats it: verified by execution, `czNjcmV0LXB3LTk=` is absent from `AGFsaWNlQGV4YW1wbGUuY29tAHMzY3JldC1wdy05` while the secret is present at a 1-byte offset — base64's 3-byte grouping means the password's bytes never start at a multiple of 3 | Phase 7 §1.3 GAP 1 | latent in module 2 (it chose its own servers, so it chose LOGIN — the scrub was *provably complete there*). **Not a module 2 bug in module 2's threat model**, and that distinction belongs in the entry |
| **4** | `SMTPError`'s eleven safe `console.*` sites depend on constructor argument order — `super(reason)` makes `e.message` the internal slug, with the provider response on a separate `.smtpResponse` field. A transposed argument breaks all eleven **silently** | Phase 7 §10 T8 | present, untested. Module 3 pins it with an explicit assertion |

Entry 3's qualifier is the one to get right, because it is the difference between a bug log and a blame
log: module 2's scrub was **correct for module 2**. What module 3 found is not that module 2 erred — it
is that a property module 2 could prove from its own configuration becomes unprovable once a third party
chooses the AUTH mechanism. The manifest entry should say that, because a future reader who fixes
"module 2's AUTH PLAIN bug" without understanding it will widen a scrub for no reason and may conclude
the coverage question is settled when it is settled only for module 3.

**Entry 3 gains a fifth column as of 2026-07-31, and a sibling entry.** M3-P7-OD-2 is **ruled (a):
PLAIN is in scope**, so module 3's scrub now diverges from module 2's (§7.3's table). The entry is no
longer *"latent in both"* — it is *"fixed in module 3, latent in module 2, and deliberately not
back-ported."* State the "deliberately" part with its reason, or the next reader reads it as an
oversight: back-porting would be widening module 2's scrub for a mechanism module 2 does not speak,
which adds a maintained code path with no threat it closes. If module 2 ever gains user-supplied SMTP
hosts, this entry is the trigger to revisit — and saying *that* is more useful than a bare TODO.

**A fifth entry, from M3-P7-OD-8 (ruled 2026-07-31):**

| # | The bug, in module 2's tree | Found by | Status in module 2 |
|---|---|---|---|
| **5** | The box-side scrub is correct only by **statement ordering nothing enforces** — the report is assembled before `credential.zero()` in `stream.js`'s `finally`, and `scrubSecret` returns text **unchanged** on an already-zeroed buffer. A scrub moved after the zero returns raw provider text **and reports success** | Phase 7 §9.1 / M3-P7-OD-8 | **present, unfixed, and silent.** Module 3 fixes it two ways (assert the ordering; `scrubSecret` throws on a zeroed buffer) — a **behaviour change**, not a bug fix, hence the §7.3 divergence row and the ruling |

Entry 5 differs from entry 3 in the way that matters for the all-modules pass: entry 3 is a coverage gap
that module 2's threat model closes for it, while **entry 5 is live in module 2 today.** Module 2's
scrub can silently emit raw provider text if a future edit reorders two statements, and nothing there
would catch it. That makes entry 5 the strongest back-port candidate in the log, and the log should say
so rather than presenting five equivalent rows.

**One inconsistency to resolve while writing this:** two documents each claim the manifest's *first*
entry — Phase 0 §8 (M3-P0-OD-10) nominates module 2's README "five blocking CI gates" when CI has run
six since `drain_grace`, and Phase 2 §1.3 nominates the `contract_drift` count under a user ruling of
2026-07-30. Both are real; only one is first. Order them by discovery date and drop the word "first"
from whichever loses, or the manifest opens with a contradiction.

Also register `FW-M3-8`'s two Go vulnerabilities here rather than only in future-work: they are a
property of the **shared** `go.mod`, so they are a manifest row (same dependency, same call paths, both
codebases) as well as a register entry.

### 7.5 The record, not a sync promise

State it once, in the file's header, so nobody reads the log as a work queue: **these entries are a
record for the all-modules polish pass** — a distinct, later, cross-module milestone that is **not**
module 3's Phase 8. Phase 8's obligation is to write them down while the evidence is fresh. Fixing them
in module 2 is that pass's call, and module 2's tree is not edited by this phase.

---

## 8. RE-MEASURE THE THROUGHPUT CEILING

### 8.1 Why module 2's number cannot be inherited

Module 2 measured **370 sends/min** at lanes=8, users=8, boxes=6, with saturation at lanes=12 (322/min —
a regression, which is the saturation signal). Its own recorded caveat is that at 6 boxes the box side
was a plausible co-bottleneck, so 370 is *"this hardware's practical ceiling with 6 boxes"* and not the
Brain's isolated maximum.

Module 3's load shape differs in three ways that all push the same direction:

| | Module 2 | Module 3 |
|---|---|---|
| Users | few, large campaigns | **many**, smaller campaigns |
| Per-send dispatch overhead | pool routing, shared-pool fallback | **per-user config resolution** (Phase 6), per-user eligibility, per-user pacing register |
| Accounts per user | operator-warmed, many per pool | few per user, user-supplied |
| Per-tick work | dispatch claim + outcome ingestion + reclaim scan | the same, plus Phase 7's notification loop and Phase 5's expiry sweep |

More users at the same total send rate means more distinct per-user resolutions per tick, and Phase 6's
register is per-user in-memory state. **The prediction is that module 3's number is lower**, and the
deliverable is a *measured* number, not that prediction. If it comes back higher, that is the interesting
result and it means the pool-routing path module 2 spent per send was more expensive than per-user
resolution — worth a sentence either way.

### 8.2 The method — port `load_e11.sh`, change the ramp axis

The inherited method is sound and its two deliberate fixture mutations must both port, because without
them the test measures something else:

- **Shrink `health_metrics_window_sec`** from 3600 to the measurement window (30s). The seeded default is
  right for an operator dashboard and would dilute a 30-second burst into a per-hour rate, reading as
  near-zero.
- **Zero `send_delay_min_sec`/`send_delay_max_sec`.** The Phase 6 pacer would cap throughput at roughly
  `lanes / avg_delay` long before the Brain is stressed, confounding exactly the variable under test.
- Both restored by an exit trap. **Module 3 adds a third:** per-user overrides of those same dials
  (Phase 6) must be **absent** for the synthetic users, or the fixture silently re-introduces the pacing
  it just zeroed. That is a new trap module 2 could not have.

Everything else ports: over-provisioned accounts (`N_ACCOUNTS = MAX_LANES × 2`) with **real encrypted
credentials** so the decrypt path is genuinely exercised; N real box **containers** with distinct
provisioned identities (OD-15 option (b) — a shared identity makes the boxes non-independent and defeats
"the box side is never the bottleneck"); Mailpit as the fast sink; bulk SQL seeding rather than
per-row round trips; `exit 0` always, because this is a scale gate and not a pass/fail.

**The one substantive change: ramp users, not just lanes.** Module 2 ramped `(lanes, users)` together
1/2/4/8/12 with users == lanes. Module 3's cost driver is **user count**, so the ramp must vary users
independently — e.g. hold lanes at 2 and ramp users 10 → 50 → 200 → 500 — or the measurement cannot
distinguish *"the Brain is lane-bound"* (module 2's finding) from *"the Brain is user-bound"* (module 3's
likely one). Keep one module-2-shaped ramp for comparability, then add the user-axis ramp that answers
module 3's actual question. Two ramps, two tables, one document.

**Saturation signal, unchanged and worth restating because it is subtle:** a **rate** plateau or
regression with boxes still `active`. Not backlog depth — each step seeds more volume than the last, so a
growing queue is expected regardless of saturation. Rate is the only step-over-step comparable.

### 8.3 Recording it

`Docs/load-test-results.md`, **appending a dated entry, never overwriting** (module 2's OD-7 ruling; it
is a versioned artifact, not a dashboard). Record the hardware, the held-constant config, both ramps'
tables, the max observed rate, the `(users, lanes, N)` at saturation, and the caveats — including whether
the box side was again a co-bottleneck at the box count used. `capacity-planning.md` then derives from
the measured number rather than restating it.

Not a CI blocker: `workflow_dispatch`-only, run before a capacity increase. The `real-infra` workflow
stays as it is.

---

## 9. SEQUENCING, AND WHAT BLOCKS WHAT

Not a section the brief asked for, but §4.3(c) makes it necessary: three of the eight sections cannot be
written correctly before specific rulings land, and writing them anyway produces confidently wrong docs.

> **Status 2026-07-31: NOTHING IN THIS TABLE IS STILL BLOCKED.** Every module 3 open decision is ruled
> (`Docs/open-decisions.md`; the needs-user queue is empty). The table is kept — with each row's
> resolution — because *what would have gone wrong* is the record of why the sequencing mattered, and
> because a future reader needs to know these sections rest on rulings rather than on Phase 8's judgment.

| Section | Blocked on | What goes wrong if written first | Resolution |
|---|---|---|---|
| §1.5 residual-risk table | **M3-P7-OD-2** (AUTH PLAIN in scope?) | the *"we authenticate to servers we do not control"* row is either bounded-by-test or bounded-by-structure, and those are different risk statements | **RULED (a): PLAIN in scope.** The row is **bounded by test** — §1.5, written |
| §6.3 error envelope + `error.code` table | **M3-P7-OD-1**, **M3-P3-OD-3** | the inventory records the widening as contract; if the widening is refused, the inventory documents a surface that does not exist | **RULED: allow**, conditional on the scrub landing first. §6.3 records the widening **and** the condition |
| §6.3 eligibility documentation | **M3-P4-OD-3** | six sites or one — and this is the fifth consecutive phase to work around it | **RULED (a): consolidate**, in Phase 4, own commit. §6.3 documents **one** definition + the DDL copy |
| §2.3 From-gate copy audit | **M3-P4-OD-1** (ruled) | not blocked — the ruling landed 2026-07-29 | unchanged |
| §4.3(a) ledger audit; §7.3 divergence table | **M3-P7-OD-9** (raised 2026-07-31 by the cross-plan sweep) | the `notifications` shape is documented from whichever phase doc the writer happened to read, and with two create sites the chain does not apply at all — so §8.1's clean-slate reproduction cannot be green regardless of what §4.3 says | **RULED (b) same day: Phase 7's shape, `0033` the single create site.** Added to this table because it is the one blocker that did **not** exist when the table was written — evidence that this class recurs and that the table is not closed by being complete once |

**The residual sequencing duty this leaves Phase 8** is not a blocker but a verification: two of the
three resolutions are *conditional or ordered*, so Phase 8 checks the order held rather than assuming
it. Specifically — that the envelope widening did not ship before the scrub (Phase 7 §12 criterion 6a),
and that the consolidation landed as its own commit before either new term (Phase 4 §8.1's three-commit
order). Both are checkable from git history, which is the point of having required separate commits.
**A third duty, from the row added above:** confirm `0046` (renumbered from `0044` by M3-P5-OD-7's
cascade, then from `0045` by M3-P6-OD-6's cascade) creates no type and no table (Phase 7 §12
criterion 15) — checkable from the migration file
itself, and the one item here whose failure mode is total rather than partial.

Order within the phase, dependency-first (module 2's chunk order, for the same reason): **§3 reorganise
and consolidate the ledger** → **§4 partition it** → **§1 + §2 write the docs** → **§5 sweep** →
**§6 + §7 capture** → **§8 measure** → **§8.1 clean-slate reproduce**. Reorganisation is first because
every doc links to paths, and last-minute path changes invalidate the link sweep (§3.4) and the
reproduction (§10 exit criterion 1) that both depend on them being final.

---

## 10. SELF-CHECK, ANSWERED

The five standing items, run against this document rather than asserted.

**1. Does every new test have a control that fails when the property is broken?**
Phase 8 adds no behavioral test — it adds **gates and proofs**, and the same rule applies. §2.4's copy
ledger gate needs the `config_consumption` negative control: an entry removed from the ledger while its
string remains in code must make the gate **fail**, observed, not assumed. §5.3's first-run empty state
is a proof and its control is the distinguishing case — *ready and idle* must be observably different
from *stopped*, or the assertion passes against a dead Brain. §6.4's `contract_drift` count, if it
becomes an assertion, needs the control that makes a wrong count fail — which is the entire reason to
change it from a print.

**2. Does anything claimed as reuse genuinely share code?**
§8's load script is claimed as a port of `load_e11.sh`, and it is — with three named fixture changes
(§8.2), one of them new to module 3. §2.4's gate is claimed as the `config_consumption` shape, and it is
the third clone of that shape, which is worth noticing: three clones of a shell script is a shared
library asking to exist, and turning it into one is **new work** (future-work, not Phase 8). §1's five
docs are claimed as ports and four of them are; §1.5 is explicitly **not**, which is stated rather than
smuggled.

**3. Does every new config key have a real consumer at its seeding commit?**
**Phase 8 seeds no config key.** Nothing can be seeded-but-unconsumed. The inverse obligation is the live
one: §1's config documentation must cover **every** dial, and the count is a fact — module 2's inherited
54, minus **two** deleted keys — `default_shared_pool_id` (Phase 2, verified three ways in Phase 1 §2.4)
and `quarantine_threshold` (Phase 5's `0042`, per M3-P5-OD-1's ruling) — plus Phase 3's 6, Phase 4's 7,
Phase 5's 10, Phase 6's 0 (it makes six existing keys overridable and seeds none — its own §8 says
*"**No new key**"*), Phase 7's **10**. That is **85** (54 − 2 + 6 + 7 + 10 + 0 + 10), and it is the number
`config_drift` and `config_consumption` must both agree with.

> **Arithmetic updated 2026-07-31 (M3-P5-OD-1).** This read *"54 − 1 … Phase 7's 9"* — the pre-ruling
> attribution, which this document's own §12 already superseded. The **total is unchanged at 85**, and
> that is the point worth noticing: the ruling *split* a key rather than adding or removing one, so
> Phase 5 drops `quarantine_threshold` and Phase 7 gains `score_alert_threshold`. Only the breakdown
> moved. A gate checking the total alone would not have caught the stale breakdown — which is why the
> per-phase attribution is stated here and not just the sum.

Phase 5 is the one to watch: its heading says *"eight dials"* while its table lists **ten**, and its own
prose concedes *"Ten, not the brief's eight"* — the table is authoritative, and the heading is a defect
Phase 8 fixes while it is counting anyway. Two inherited keys are already `RESERVED / NOT-YET-CONSUMED` (`retry_count_4xx`,
`variation_zero_width_rate_pct`), both found **by accident** in a module 2 Phase 9.5 sweep, and Phase 2
§1.2.1 time-boxed the RESERVED hatch precisely so it does not become permanent — so Phase 8 must check
whether those expiries have passed, which is a gate failure, not a doc nit. Also inherited: module 2's
`config-reference.md` prose says "53-key" and `architecture-overview.md` says "53 seeded keys" while the
real count is 54; Phase 1 §2.4 flagged both for correction in the rename pass, and Phase 8 verifies the
correction landed rather than assuming it did.

**4. Does every changed object have its dependents listed?**
The changed objects in this phase are **doc paths**, and §3.2 enumerates their dependents as a table —
six gates, five with `Docs/` literals, plus the README's own coupling table. That is the dependent list,
and it is the one thing in this phase that breaks CI if it is incomplete. §6.4's route-count literal and
§7.2's schema mismatch are the other two changed objects, each with its dependents named inline.

**5. Did I re-specify anything inherited instead of referencing it?**
Checked, and it corrected two drafts. The living/history split, the *link-don't-restate* rule, the
`config_consumption` algorithm, `load_e11.sh`'s method, the go-live checklist's 11-item shape, and
module 2's security §1/§2 tables are all **referenced** with their divergences named, not restated.
Where this document does quote at length it is deliberate and marked: §6.2's nine format conventions
(because "match module 2's format" is unactionable without them, and §6.1 argues one of them should
change), §1.5's residual-risk framing (because the *audience* changes and the argument needs the
original to push against), and §7.1's two disclaimers (because softening them is the failure mode).

---

## 11. EXIT CRITERIA

The build plan's five, verbatim, plus what this document adds.

1. **"Clean-slate install reproduced from the docs alone"** — a genuine fresh `git clone`, the README's
   commands verbatim, **zero deviation**, timed, full harness green. Module 2's standard, and the word
   that carries it is *reproduced*: a walkthrough by someone who already knows the answer is not a
   reproduction. **Module 3's version goes further than module 2's, because §1.1 changed the last step:**
   the reproduction must end with a **user** journey — create a user, add an account through the Phase 3
   flow with a real credential, pass the From gate, and send — not an operator provisioning their own
   account. If the docs alone cannot get a user to a sent message, the docs are not done.
2. **"Every config dial documented with default, range, consumer, and effect"** — all **85** (self-check
   3), `config_drift` and `config_consumption` both green, and **no RESERVED marker past its expiry**.
   The brief adds *range* and *consumer* to the build plan's *default and effect*; `config-consumers.txt`
   already carries consumer, so *range* is the genuinely new column and it must be real (a range nobody
   validates is a comment).
3. **"Open decisions empty or moved to Future Work"** — satisfied by §4.3's **four-way partition with a
   count**, not by an empty file. Every one of the **80** sits in exactly one disposition; every
   `AWAITING RULING` entry names what it blocks. **Counts updated 2026-07-31:** all **80** are ruled —
   15 closed by an earlier ruling, 2 merged, 62 in the 2026-07-31 pass, plus M3-P7-OD-9, raised by that
   pass's cross-plan sweep and ruled the same day — so the "64 unruled" figure this criterion used to
   cite is historical, and `AWAITING RULING` should come out **empty**. **Rule 2 still reserves closure
   to the user, and that has not softened:** a Phase 8 that reports "empty" without a ruling for each
   item is lying. The number it must not fabricate is now **0**, and 0 is a claim to *verify* against
   the ledger, not the comfortable default — an item surfaced by implementation is a new decision for
   the ledger, not an inconvenience to round down.
3a. **The consolidated ledger exists and has been RELOCATED to `Docs/history/open-decisions.md`** — it
   is live today at `Docs/open-decisions.md` (§4.3(a)), so this criterion is met by moving it alongside
   the phase specs in §3's reorganisation commit and repointing the nine inbound references, **not** by
   building it. It is not met while a copy remains at the old path or any register still points there.
3b. **`M3-P7-OD-9`'s ruling is APPLIED IN THE TREE**, not merely recorded — the `0033`/`0044`
   (renumbered `0045` by M3-P5-OD-7's cascade, then `0046` by M3-P6-OD-6's cascade)
   `notifications` collision, **ruled (b) 2026-07-31**
   (Phase 7 §13). Separated from criterion 3 because
   it is **not** a documentation item: with two create sites the migration chain does not apply at all,
   so criterion 2's clean-slate run cannot be green. The check is mechanical and must be **run**, not
   read: `byosmtp_0046_*.up.sql` contains no `CREATE TABLE` and no `CREATE TYPE`, and
   `byosmtp.notifications` exists after `0033` with `condition`/`subject_type`/`payload`/`state`/
   `notified_at`. A ✅ in a register is not evidence (Rule 11).
4. **"Interface inventory committed in module 2's format"** — with **M3-P8-OD-2 ruled** (§6.1: module 2
   has no inventory document; `api-contract.md` is it, and its by-phase grouping does not align across
   modules). All nine format conventions from §6.2 reproduced, `contract_drift` green against the served
   surface, and the three-audience boundary stated so the later comparison does not read
   `/v1/byosmtp/*` as a mismatch.
5. **"A measured ceiling recorded"** — appended and dated in `load-test-results.md`, **both ramps**
   (module-2-shaped for comparability, user-axis for module 3's actual question), with the saturation
   point and the honest caveat about whether the box side was again a co-bottleneck.
6. **All CI gates green, including the doc-path drift gates after reorganisation** — and the
   reorganisation and the gate edits are **one commit** (§3.2(a)), because module 2 already learned that
   one. Six gates, not five: `config_drift`, `contract_drift`, `migration_lint`, `config_consumption`,
   `drain_grace`, `no_secret_leak` — plus Phase 7 §1.6's user-output scrub gate and, if §2.4 lands, the
   copy-ledger gate. **Count the gates in the README's table and assert the table names real scripts**,
   or fix the count module 2 got wrong (M3-P0-OD-10: its README says five, CI has run six since
   `drain_grace`).
7. **The security posture is honest about third-party custody** — §1.5's four named residual risks are
   stated as risks, not as mitigated hypotheticals, and the page names its **two audiences** (§0.1).
   The test: hand it to someone who is deciding whether to give this system their Google app password,
   and see whether it tells them what they need to know. Module 2's page passes that test for its own
   operator; a rename of it does not pass for a user.
8. **Every user-facing string is audited against the evidence it actually holds** (§2.2), enumerated in a
   ledger, and **no string asserts more than the module can observe**. Phase 4's ruling generalised:
   *"we couldn't verify this will authenticate"*, never *"this will fail."*
9. **The shared-code manifest states what actually diverged**, per area, verified by `git diff` against
   the fork point rather than by Phase 0's forecast (§7.3) — with the box row checked explicitly, since
   it was forecast as untouched and three phases reach into it.
10. **The FIVE module 2 bugs are logged as concrete entries** (§7.4 — the brief's four plus **entry 5**,
    the scrub's unenforced statement ordering, added by M3-P7-OD-8's ruling), with entry 3 carrying its
    threat-model qualifier **and its "fixed in module 3, deliberately not back-ported" status**, entry 5
    marked as the strongest back-port candidate because it is live in module 2 today, and the two
    competing "first entry" claims (§7.4) ordered by discovery date.
10a. **The all-modules polish-pass entries include *re-axe module 2's `api-contract.md`*** (M3-P8-OD-2's
    ruling) — module 3 adopts module 2's format but not its by-phase axis, and the mismatch is module 2's
    half to fix. A separate criterion because it is an entry about **module 2**, which the rest of §7.4's
    log is too, and it is the one that would otherwise be filed as a module 3 doc task and lost.
11. **A `go-live-checklist.md` in module 2's shape** — numbered items, each with *Why it blocks* and
    *Action*, an *Order of operations*, and a `- [ ]` final pre-flight. Module 3's items diverge where
    the custody model does: no `default_shared_pool_id` to set, no operator accounts to pre-warm, and
    **two new items with no module 2 counterpart** — *confirm the master key is backed up **and** that
    its rotation has no operator-side path* (§1.3, `FW-M3-4`), and *decide the `send_logs` retention
    window you can defend to a user whose message bodies it holds* (§1.5 R3).
12. **No behaviour changed.** The hygiene sweep touched logging only; the copy audit touched strings
    only; every capability the audit revealed went to future-work with a trigger. A Phase 8 diff that
    changes a code path is out of scope by the brief's own terms — *"If polish reveals a missing
    capability, FLAG it into future-work.md — do not build it."*

---

## 12. OPEN DECISIONS REGISTER

> **Consolidated queue: [`Docs/open-decisions.md`](open-decisions.md).** That file is where these are
> ruled from — it records which of them a later phase's ruling already closed, and which bucket
> (BLOCKS / RULE / ACCEPT) each remaining one sits in. This register stays authoritative for full
> context. It is also the deliverable **M3-P8-OD-4** recommends: §4.3's partition, built.

| ID | Question | Where it bites | Recommendation |
|---|---|---|---|
| **M3-P8-OD-1** ✅ **RULED-ACCEPTED 2026-07-31** | The future-work **merge format**. Module 2's is a 3-column table with ~31 entries; module 3's is labeled prose with a 7-field schema (`FW-M3-N`, Status, Trigger to revisit, Interaction to preserve) and 1 entry | §4.1. The merge is mandated by module 3's own file (*"at fork time … two registers is exactly the staleness Rule 4 exists to prevent"*) but the resulting format is unspecified | **Keep module 2's section taxonomy, adopt module 3's per-entry schema for new/touched entries**, as `### FW-M3-N` subsections inside the relevant section. Back-filling seven fields onto 31 inherited entries is invented provenance; flattening module 3's discards the two fields that keep a register from rotting. State the mixing rule in the header so it reads as a decision. Defensible default → should not block. **Approved as recommended** |
| **M3-P8-OD-2** ✅ **RULED 2026-07-31** | **The interface inventory's organising axis.** The brief says *"match module 2's inventory format"*; module 2 has **no inventory document** — `api-contract.md` is it, fusing inventory with contract and grouped **by the phase that built each route** | §6.1. This is the input to the all-modules comparison. By-phase grouping aligns headings that share a name and nothing else across modules (module 2's "Phase 3" is the minimal send path; module 3's is user-owned accounts), so a section diff is noise | **Adopt module 2's format exactly — all nine conventions (§6.2) — and change only the top-level grouping**, to auth domain then resource path, with a per-route **Phase** column so provenance is kept but is not the axis. Format is what makes a diff mechanical; grouping is what makes it align. This deviates from the brief's literal wording, hence a ruling. **RULED: as recommended — adopt module 2's `api-contract.md` FORMAT but NOT its axis.** Grouping is **auth domain → resource path**, with **phase as a column**. Additionally: *"re-axe module 2's `api-contract.md`"* joins the **all-modules polish-pass entries** (§7.4), because the axis problem is module 2's as much as module 3's — a comparison whose two halves are organised differently is a comparison nobody runs twice |
| **M3-P8-OD-3** ✅ **RULED-ACCEPTED 2026-07-31** | Does the **user-facing copy ledger** (§2.4) ship as a **blocking gate**, or as a reviewed table without one? | §2.4. Precedent cuts both ways: Phase 7 §1.6 made a closure grep a gate on the reasoning *"a gate proves it forever"* — but copy honesty is not mechanically checkable, so the gate can only assert *enumerated*, never *honest* | **Ship it as a gate, scoped to what it can prove:** every string is in the ledger, every ledger line names the evidence it asserts. That converts *"someone audited the copy once"* into *"no string can be added without declaring its claim."* The honesty column stays human, and the gate's own doc says so — a gate that overstates its guarantee is worse than none. **Approved as recommended** |
| **M3-P8-OD-4** ✅ **RULED-ACCEPTED 2026-07-31 — and OVERTAKEN by events** | §4.3's finding: **64 of 73 open decisions are unruled**, and Rule 2 reserves closure to the user. Is Phase 8 **blocked** on obtaining those rulings, or does it ship with the four-way partition and an `AWAITING RULING` list? | §4.3, §9. Exit criterion 3 either means "empty" or means "partitioned honestly." Three of Phase 8's own sections are blocked on specific rulings (§9) regardless | **Approved as recommended: ship with the partition** — and the recommendation's second half was *acted on rather than deferred.* The count is now historical: **all 80 module 3 open decisions are ruled** and the needs-user queue is **empty** (`Docs/open-decisions.md`, 2026-07-31), including all **four** of §9's blockers — 80 and four, not 79 and three, because the plan-revision sweep raised **M3-P7-OD-9** after this row was written and it was ruled the same day. That the number moved *upward* before reaching zero is this row's own point restated by events: the queue empties by disposal, and disposal includes items nobody had found yet. So the partition remains the method and the deliverable, but `AWAITING RULING` should come out **empty** — which turns exit criterion 3 from "partitioned honestly" into the stronger "empty," achieved rather than excused. If Phase 8 finds a member of that class it is a decision **surfaced by implementation**, and it goes to the ledger |
| **M3-P8-OD-5** ✅ **RULED-ACCEPTED 2026-07-31** | Module 3's **master key cannot be rotated as an operator action** (§1.3). Is that acceptable for V1, stated as a residual risk — or does it gate go-live? | §1.3, §1.5. Module 2's procedure requires every account's plaintext, which module 3's operator has never held. There is no side channel and no fallback | **State it, do not solve it in Phase 8** — a rotation campaign against the whole user base is a feature, and the brief forbids building one. But it is more than a future-work line: it means a key compromise has **no recovery path short of every user re-entering a secret**, so it belongs on the go-live checklist as an accepted risk with a signature, not only in the register. `FW-M3-4`. **Approved as recommended** |
| **M3-P8-OD-6** ✅ **RULED-ACCEPTED 2026-07-31** | Should the `Docs/README.md` **doc↔gate table become machine-checked** (a seventh gate asserting each named doc exists and each named gate is a real script)? | §3.2(b). It is the only manifest of doc↔gate coupling and nothing verifies it — so the artifact a future phase would consult to know which gates to repoint can go stale silently | **Register as `FW-M3-6`, do not build in Phase 8.** It is a new gate, which is new work. Flagged because it is small enough to be tempting and because the reorganisation this very phase performs is exactly the event it would have protected against — which is an argument for building it, and the user's call to make. Phase 8 must at minimum update the table in the same commit. **Approved as recommended:** register as `FW-M3-6`, do not build; update the table in the reorganisation commit |

**Inherited open decisions this phase depends on, not owned by it:**

**All of these are RULED as of 2026-07-31** (`Docs/open-decisions.md`; needs-user is empty). Each note
below now states the ruling and what Phase 8 does with it, rather than the branch it was waiting on.

- **M3-P7-OD-1 / M3-P3-OD-3** (the send- and verify-surface envelope widening) — **RULED: allow
  `smtp_response` + `error_class`, CONDITIONAL on M3-P7-OD-2's scrub landing first.** §6.3 records the
  widening as module 3's most consequential deliberate divergence from the shared contract, **and records
  the condition alongside it** — the field without the scrub is a leak, so a future module copying the
  envelope must copy the guarantee. Note the provenance correction: OD-3 was never independently ruled
  and is settled **by** OD-1 a fortiori, not the reverse (Phase 7 §1.1, corrected 2026-07-31).
- **M3-P7-OD-2** (AUTH PLAIN) — **RULED (a): PLAIN IS IN SCOPE.** §1.5's *"we authenticate to servers we
  do not control"* residual risk is therefore **bounded by test**, with the bound named (all three
  base64 alignments plus the full blob, five paired controls). The reasoning to preserve: *users bring
  their own servers, including custom ones, so refusing PLAIN breaks real users for a security
  convenience.* Consequences for this phase beyond §1.5: a §7.3 divergence row and §7.4's entry 3
  requalified as *fixed in module 3, deliberately not back-ported*.
- **M3-P7-OD-8** (the scrub's unenforced statement ordering) — **RULED: BOTH.** Assert the ordering
  **and** make `scrubSecret` throw on a zeroed buffer, *"fail-closed beats fail-silent on a credential
  path."* New for this phase, because it is a **behaviour change to shared box code**: it lands as a
  §7.3 divergence row and as §7.4's **entry 5**, which is the strongest back-port candidate in the log
  since it is live in module 2 today.
- **M3-P7-OD-4** (the omit-vs-null report inconsistency) — **RULED-ACCEPTED as recommended.** Phase 7
  deferred it *"for Phase 8's interface comparison, where the whole family can be reconciled at once."*
  §6.3 records it; it still does not fix it, because a client distinguishing absent from null would break.
- **M3-P4-OD-3** (the eligibility predicate) — **RULED (a): CONSOLIDATE**, in Phase 4, as its own commit
  before either new term. §6.3 documents **one** definition plus the DDL copy that could not be absorbed.
  Two changes for this phase: `FW-M3-2` is **closed rather than registered** (§4's table), and Phase 8
  gains a verification duty — confirm from git history that the consolidation was a separate commit
  (§9), since that separation is the whole reason a failure would be attributable.
- **M3-P5-OD-1** (the orphaned `quarantine_threshold`) — **RULED as a split: DROP it, ADD
  `score_alert_threshold`** with a real consumer in Phase 7's warnings. Bears on Phase 8 in two places:
  the **config dial total stays 85** but per-phase attribution changes (inherited 52, Phase 7 now 10),
  which both `config_drift` and `config_consumption` must agree with; and the deletion needs an **ADR
  narrowly extending module 2's no-key-removal exemption** on the stated basis — the key never had a
  consumer in module 3, so this is removing dead inheritance, not breaking a live contract — with the ADR
  stating explicitly that it does **not** reopen key removal generally. Phase 8's doc sweep verifies that
  ADR exists and says that.
- **M3-P0-OD-9** (scope of module 2 provenance carried across) — §3.1's `Docs/history/module-2/` contents
  are exactly what that ruling admits.
- **M3-P0-OD-10** (module 2's README says five CI gates; CI has run six) — §7.4's competing "first
  manifest entry" claim, and exit criterion 6's gate count.
- **M3-P2-OD-1** (`default_shared_pool_id`'s RESERVED disposition and the time-boxed expiry check) —
  self-check 3's *"no RESERVED marker past its expiry"* is that check, and Phase 8 is the phase where an
  expiry set in Phase 2 would plausibly have passed.
- **Phase 6's T7** is still owed as a test (per the Phase 6 record). Phase 8 verifies it landed rather
  than assuming; a phase that closes the project while a specified test is missing has not closed it.
