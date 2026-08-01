# Phase 4 — The From gate

> **Status (2026-08-01):** Chunk 1 (the DNS evaluation primitive, `internal/fromgate`) is
> implemented and green against real local-authoritative-DNS infra (`deploy/dns-test/`, the
> `coredns`/`dns-blackhole` compose services) — 15/15 tests, including T1-T5, T2b, T3c, T4, the
> SPF lookup/void-limit chains, macro handling, ambiguous-DMARC non-detection, and a live canary
> against `_spf.google.com`. Migration `0039` (the seven config seeds) is written and its keys
> have real consumers in this chunk's code. Two new config-ordering invariants
> (`from_dns_lookup_timeout_ms <= from_dns_total_budget_ms`,
> `from_dns_negative_cache_ttl_sec <= from_dns_cache_ttl_sec`) are added to
> `internal/configordering`, shared with the (not-yet-built) admin PUT boundary per the existing
> P9-OD-2 precedent. **One genuine spec gap found and ruled while running real tests, not while
> reading the spec:** M3-P4-OD-13 — §3.2's "NXDOMAIN on the From domain itself is decisive" row
> cannot be implemented against Go's stdlib resolver (`net.DNSError.IsNotFound` is set identically
> for NXDOMAIN and NODATA, confirmed empirically against a legitimate SPF-publishing domain with no
> A record); dropped, absence now falls through to the ordinary WARN path. `from_domain_not_found`
> is removed from the external-code vocabulary. **M3-P4-OD-12** (found before writing code, not
> during testing) settled what `from_gate_mode=off` writes (`aligned`, disclosed as skipped) so the
> escape hatch actually unblocks routing. Chunks 2 (the store-layer gate + acknowledgment) and 3
> (failure-mode wiring + the test-button hook) are not yet built. See `Docs/open-decisions.md` for
> both rulings' full reasoning.
>
> **Chunk 2 update (2026-08-01):** the store-layer gate landed on all four §2.2 write routes —
> `platformapi` create/PATCH/bulk-add (Domain A) and `adminapi` create/PATCH (Domain B) — plus
> M3-P4-OD-9's break-glass override on both admin routes. §2.8's finding (admin PATCH could
> preserve a stale `aligned` verdict across an ungated `from_address` change) is closed: both
> surfaces' PATCH handlers re-run the gate exactly when `from_address` changes, refuse an
> unacknowledged WARN or an indeterminate check (§2.3/M3-P4-OD-15), and the override writes
> `'unverified'` only, never a real verdict, with its own audited event. 27 new behavioral tests
> (18 real-infra against `platformapi`/`adminapi` plus `internal/fromgate`'s own copy-rendering
> unit tests) green, including T8/T8b/T8c/T13/T13b and both create-time rulings (M3-P4-OD-14/15).
> This needed a THIRD migration — M3-P4-OD-16, ruled the same day: the override's own audit event
> (`'from_gate_overridden'`) required widening `account_events_action_check`, which Phase 4's
> original two-migration allotment never anticipated. Phase 4 now claims `0039`–`0041`; Phase 5+
> cascaded +1 again (the third such cascade this build, after M3-P3-OD-18/19) — see
> `Docs/plan-revision-log.md`'s 2026-08-01 addendum for the full chain.
>
> **Chunk 3 update (2026-08-01):** the test-button hook (§2.5) landed — `POST .../test` re-runs the
> From gate against the account's CURRENT stored From/host/username unconditionally, reports both
> verdicts (credential + alignment) independently per M3-P4-OD-18 (a nil/erroring credential
> verifier now surfaces as `verification.state="unavailable"` inside a 200, never a whole-request
> 500 that would hide an already-persisted From-gate result), and always persists a fresh verdict
> even when unacknowledged (M3-P4-OD-17), leaving the account non-routable rather than refusing the
> call. §7.2's remaining test-table rows are closed: **T9** (`internal/dispatch`'s eligibility
> predicate excludes `blocked` and unacknowledged `unaligned_warned`, with same-row controls
> restoring routability), **T10** (already satisfied by Chunk 1's own deterministic `cache_test.go`
> — confirmed, no new test needed), **T11** (the two Chunk 1 config-ordering invariants get their
> own pure unit tests in `internal/configordering`, including the `==` boundary-accepted control),
> **T13c** (re-acknowledgment is required only when the verdict genuinely CHANGES, not on a mere
> re-run — proven purely by data via the GENERATED `from_verdict_fingerprint` column, with both of
> §7.2's required controls), and **T6** (dispatch never resolves DNS: a static test asserts
> `internal/fromgate` is absent from `internal/dispatch`'s real `go list -deps` production
> dependency graph, and a behavioral test proves an `aligned` account keeps dispatching with a
> negative control on an `unverified` one — both landed in `internal/dispatch` rather than the
> `tests/harness` shell suite this section originally called for, since the dispatch package's own
> Go test harness already drives a real `TryDispatch`/`recordingPusher` round trip end-to-end and
> needed no live box/Mailpit to prove the property). 6 new tests across
> `internal/{dispatch,configordering,platformapi}`, all green. Phase 4's implementation is now
> complete pending the scoped agent-review pass (§2's bypass-surface question) and phase-end
> verification.
>
> Original status below, retained for what it originally meant:
>
> **Status (original):** PLAN ONLY. No code, no migrations, no tests written by this document.
**Owns:** the SPF/DMARC alignment gate on `byosmtp.accounts.from_address`, the four setup-time entry
points that must pass through it, and the routing exclusion that makes a `blocked` verdict mean
something.
**Migration numbers:** `0039` (config seeds), `0040` (routing-predicate change + acknowledgment
columns), and `0041` (M3-P4-OD-16: widen `account_events_action_check` for the operator override's
own audit action). Phase 1 claims `0025`–`0034`; Phase 3 claims `0035`–`0038` (amended twice,
M3-P3-OD-17/18 then M3-P3-OD-19 — C3 needed the narrow `job_items` INSERT grant, C4 needed the
`bulk_add_max_rows` seed, both discovered only while implementing; see
`Docs/phase-3-user-owned-accounts.md` §0.2).

**The order is load-bearing, and an earlier draft had it reversed.** The config seeds must land
*first*: §6.1's keys are consumed by the evaluator, and §8.2's recommended two-step deploy ships the
evaluator (and so the entry points, and so the config reads) **before** the predicate. Seeding the
keys after the predicate would mean a Brain whose evaluator loads config that does not exist yet — and
a missing config key is a **hard error** at load, not a fallback (`dispatch/config.go:256-257`: *"A
missing key is still a hard error (the migration seeds it); only a corrupt/edited value triggers the
fallback"*). That is a fatal startup, not a degraded one. Numbering the seeds `0040` would have made
the recommended deploy sequence impossible to execute.

---

## 0. START BY SUBTRACTING — what already exists

Phase 1 already built this phase's entire storage layer. Phase 3 already built the transport it
re-uses. What is genuinely new is **the evaluator, the entry-point wiring, and the routing
exclusion** — nothing else.

### 0.1 Phase 1 §4.4 supplied every column, including the one that makes the hard rule possible

`Docs/phase-1-schema-delta.md:604-646`, migration `0026`. Verified in the tree:
`from_verification_state` (`unverified|aligned|unaligned_warned|blocked`), `from_dmarc_policy`
(`none|quarantine|reject`, NULL = no record), `from_spf_aligned`, `from_dmarc_aligned`,
`from_checked_at`, `from_last_attempt_at`, `from_last_attempt_error`, `from_check_detail` JSONB,
`accounts_from_checked_pairing_check`, `accounts_from_attempt_error_len_check` (≤2000).

**The two-timestamp split is this phase's load-bearing inheritance.** Phase 1 §4.4 states the reason
in its own words: *"A single 'checked_at' column forces a failed lookup to either overwrite the
verified verdict (re-blocking — the forbidden behavior) or leave no trace of the attempt
(undiagnosable)."* §4 below is the behavior that split exists to enable. This phase does not
re-specify the columns; it specifies who writes them and when.

### 0.2 Phase 3 supplied the test button, and it is a hook, not a re-implementation

Phase 3 §3 owns `POST /v1/byosmtp/accounts/{id}/test` and its `push_verify`/`verify_report` frame
pair with `stopAfterAuth: true`. Phase 4 **adds a second, independent check to that route's
response** and does not touch the frame pair, the box, or the SMTP path. Explicitly:

- **No new protocol frame.** The DNS check runs entirely on the Brain. The box has no DNS access, no
  resolver, and learns nothing about alignment — consistent with the box's structural isolation
  (CLAUDE.md Rule 6).
- **No change to `stopAfterAuth`.** Credential verification and From verification are two
  independent verdicts stored in two independent column groups (`credential_*` from Phase 3 §2.4,
  `from_*` from Phase 1 §4.4). They can disagree, and both are reported.

### 0.3 What does NOT exist and is real new substrate

Verified by grep across module 2's tree (`LookupTXT|LookupMX|LookupIP|net.Resolver|LookupCNAME|spf|dmarc|dkim`):
**zero DNS code anywhere.** `services/brain/go.mod` requires only `pgx/v5` and `golang.org/x/crypto`
— no DNS library, no Public Suffix List package. The harness has no DNS fixture; `services/box/test/sink.mjs`
is SMTP-only. §7 therefore specifies new test infrastructure, not a new assertion against existing
infrastructure.

### 0.4 The one architectural fact that shrinks this phase

**The RFC5321 envelope `MAIL FROM` and the RFC5322 `From:` header carry the same single value.**
Traced end to end in module 2: `dispatch.go:357 FromAddress: acct.fromAddress` → `boxproto.go:86` →
`stream.js:322 envelopeFrom: pj.send.from_address` → `smtp.js:206 MAIL FROM:<${job.envelopeFrom}>`;
and the same value → `headers.go:23 "From": formatFrom(fromName, fromAddress)`. There is no separate
Return-Path and no bounce address.

DMARC evaluates SPF alignment against the envelope-sender domain and DKIM alignment against the
header-From domain (RFC 7489 §3.1). Because both are `accounts.from_address`, **the two identifiers
cannot disagree**, there is no envelope/header split to configure, and there is exactly one column to
gate. Every "which domain do we check" ambiguity in the general DMARC problem is closed by
construction here.

---

## 1. What "alignment" means operationally

### 1.1 The check the recipient actually performs — and why the obvious implementation is wrong

The gate's job is to predict a verdict reached by a machine we never talk to. Getting the mechanics
right matters more here than anywhere else in the phase, because the naive implementation fails in
the worst possible direction.

When the user's provider relays the message, the **recipient's** MTA sees:

- the IP of the **connecting peer** — the user's provider's *outbound relay* MTA, and
- the domain asserted in `MAIL FROM` — the From domain.

It fetches **the From domain's own SPF record** and asks whether it authorizes **that relay IP**.

**The submission endpoint we connect to is not that relay.** Confirmed empirically, twice, against
`8.8.8.8`:

```
smtp.gmail.com  → 142.250.27.109        (a second resolution, another day: 142.251.168.109)
_spf.google.com → v=spf1 ip4:74.125.0.0/16 ip4:209.85.128.0/17 ip6:… ~all
```

Neither address falls inside `74.125.0.0/16` or `209.85.128.0/17`. Google's AUTH submission
front-end and its outbound relay fleet are **disjoint address pools**.

**Consequence, and the single most important design constraint in this phase:** the intuitive
implementation — *resolve `smtp_host`, run `check_host()` with that IP against the From domain's SPF
record* — **BLOCKS every correctly configured Gmail user.** Their domain's SPF is right, their mail
will genuinely pass DMARC at the recipient, and we would refuse it. That is a false BLOCK: the
failure mode that looks to a paying user like the product is broken, and it is strictly worse than a
missed block, because a missed block is a known monitorable gap while a false block destroys trust
in the gate itself. This design is **rejected here by name** so it is not rediscovered as a
"simplification" in a later phase.

Using our own box's egress IP is worse still — a category error. Our box never appears in the
session between provider and recipient. It would fail for essentially every domain on earth.

### 1.2 The primitive the gate actually computes

Not "simulate the recipient's SPF evaluation." Instead:

> **Does the From domain's SPF record, transitively expanded, authorize the sending provider
> identified by `smtp_host`?**

That is answerable at setup time from DNS alone, and it is the right question. Mechanically:

1. **Identify the provider** from the literal `smtp_host` string, against a curated match table
   (§1.4). This is a maintained allowlist, not a derivation.
2. **Fetch and expand the From domain's SPF record** — one TXT query at the domain, then follow
   `include:` and `redirect=` per RFC 7208, honoring the 10-lookup and 2-void-lookup limits (§3).
3. **Test for the provider's token** anywhere in that transitive graph. Present → the domain has
   authorized this provider's infrastructure. Absent → it has not.

   **This step returns two facts, not one** (required by M3-P4-OD-1's ruling — see §1.6). "No SPF
   record published at all" and "an SPF record that does not authorize *this* provider" are different
   answers, and post-ruling they decide different outcomes: only the first can contribute to a BLOCK.
   So the primitive reports **record presence** (did any `TXT` beginning `v=spf1` exist at the
   apex?) alongside **token presence** (was the provider's token found in the expansion?). Collapsing
   them into one boolean — the natural implementation, and the one the pre-ruling spec permitted —
   makes the ruling's central distinction unrepresentable, and the gate will BLOCK every SES customer
   again. The three DNS outcomes that must stay distinct: no record; record present, token absent;
   record present, token present. A lookup *failure* is a fourth and is §3.1's territory, never
   reported as absence.
4. **Fetch DMARC** — one TXT query at `_dmarc.<From domain>`, with a single organizational-domain
   fallback if the exact name yields no valid record (§1.3).
5. **Probe DKIM corroboration** where the provider has a documented, deterministic selector
   convention (§1.5).
6. **Decide** per §1.6.

### 1.3 DMARC record discovery — RFC-mandated, no latitude

Per RFC 7489 §6.6.3, all four rules are mandatory and each has a failure mode if skipped:

- A TXT record at `_dmarc.<domain>` is a DMARC record **only** if, after concatenating its strings,
  it begins with the literal first tag `v=DMARC1`. Anything else is discarded before processing.
- **Multiple `v=DMARC1` records at one name is not "pick one" — it is non-detection.** The RFC:
  *"If the RRset returned contains multiple records or no records, mail receivers MUST NOT apply
  DMARC processing."* A domain with two DMARC records has, from the recipient's point of view, no
  enforceable policy. The gate must reach the same conclusion, or it will BLOCK on a `p=reject`
  the recipient will never act on.
- **Organizational-domain fallback**, once, if the exact name yields zero valid records: take the
  longest suffix of the candidate that appears on the Public Suffix List (x labels), then add one
  more label to the left (x+1). `sp=` applies only to subdomains, never to the queried domain itself.
- **This requires a real PSL, not "the last two labels."** The naive rule is wrong for every
  multi-label public suffix: `foo.bar.co.uk` reduces to `co.uk`, which is a registrar suffix under
  which thousands of unrelated businesses register. Alignment computed against it would declare
  unrelated organizations aligned — which is precisely the spoofing case this gate exists to catch,
  inverted. Same failure for `github.io`, `blogspot.com`, and every other PSL private suffix.
  `golang.org/x/net/publicsuffix`'s `EffectiveTLDPlusOne` is exactly this primitive. Its bundled
  snapshot is a documented staleness risk (§10, M3-P4-OD-7).

Relevant tags and defaults: `p` (required), `sp` (inherits `p`), `aspf` / `adkim` (default `r`
relaxed — same organizational domain; `s` strict — exact FQDN), `pct` (default 100).

### 1.4 Provider identification — a curated table, versioned, never a derivation

Matching an arbitrary `smtp_host` to a provider has no RFC guidance. It is curation, it drifts, and
it must be treated as data with an owner rather than as logic.

| Provider | `smtp_host` match | SPF token (confirmed live) | Deterministic DKIM selector? |
|---|---|---|---|
| Google Workspace | `smtp.gmail.com`, `smtp-relay.gmail.com` | `_spf.google.com` | yes — `google._domainkey` |
| Microsoft 365 | `smtp.office365.com`, `smtp.outlook.com` | `spf.protection.outlook.com` | yes — `selector1`/`selector2._domainkey`, CNAME-delegated |
| Amazon SES | `email-smtp.*.amazonaws.com` | `amazonses.com` | **no** — random per-identity tokens |
| SendGrid | `smtp.sendgrid.net` | `sendgrid.net` | **no** — per-account CNAME (`s1`/`s2`, account-ID-bearing) |
| Mailgun | `smtp.mailgun.org`, `smtp.eu.mailgun.org` | `_spf.mailgun.org` **and** `_spf.eu.mailgun.org` | partial — `mx` or `k1`, varies by onboarding era |
| Postmark | `smtp.postmarkapp.com` | `spf.mtasv.net` | **no** — per-server |
| Brevo | `smtp-relay.brevo.com` | `spf.sendinblue.com` | **no** |
| Zoho | `smtp.zoho.*` | `zoho.com` (region variants `zoho.eu`, `zoho.in`) | **no** — region-dependent |
| Fastmail | `smtp.fastmail.com` | `spf.messagingengine.com` | yes — `fm1`/`fm2`/`fm3._domainkey` |
| Mailjet | `in-v3.mailjet.com` | `spf.mailjet.com` | partial |
| SMTP2GO | `mail.smtp2go.com` | `spf.smtp2go.com` | **no** |

Two traps in that table are worth stating rather than leaving to be discovered:

- **Mailgun's EU and US regions publish different tokens.** Matching only one produces a false
  BLOCK for every EU customer. Any provider entry is a **set** of acceptable tokens, not one string.
- **A token match proves the domain authorized the provider's infrastructure. It does not prove the
  provider will let this account send as this domain.** Google Workspace "Send As" verification,
  SES identity verification, and Mailgun domain verification are all provider-side state invisible
  to DNS. This is a hard ceiling on every DNS-only technique, not a gap in this one — and it is the
  honest reason the gate's ALLOW is "no obstacle found," never "guaranteed delivery."

**The "matches nothing" branch is the one most likely to be specified wrong, so it is specified
first: an unmatched `smtp_host` NEVER blocks.** Self-hosted Postfix, niche hosts, and vanity CNAMEs
have no reliable oracle. Blocking on an unverifiable heuristic would reproduce §1.1's worst case
across the entire self-hosted population — disproportionately small legitimate senders with the
least recourse. Unmatched hosts yield at most `unaligned_warned`, with copy that says we could not
identify the provider (§5.3).

One narrow exception, and the asymmetry behind it: if the From domain's SPF record contains `a` or
`mx`, **and** the resolved `smtp_host` is the domain's own mail infrastructure, then submission
endpoint and outbound relay genuinely are the same machine, and §1.1's objection does not apply.
Self-hosted MTAs do not separate the two. This may support `aligned` for the self-hosted case; it
still may not support `blocked`.

### 1.5 What DKIM can and cannot contribute

DKIM is not optional context — it decides whether BLOCK is legal at all (§1.6).

- **Can** probe `<selector>._domainkey.<domain>` for TXT, following CNAMEs. This is mandatory:
  M365 and SendGrid both delegate signing keys by CNAME, and a prober that does not follow them sees
  "no key" for every one of their customers. Go's `net.Resolver.LookupTXT` follows CNAMEs
  transparently.
- **Cannot** discover the selector in general. There is no `_dmarc`-style fixed name for DKIM. Where
  a provider documents a convention (Google, M365, Fastmail) we can probe it; where the provider
  mints random per-identity selectors (SES, SendGrid, Postmark) **no guess is possible even in
  principle**.
- **Cannot see a signature.** Selector presence proves the provider is configured to sign for the
  domain. It does not prove any given future message will carry a verifying, aligned signature. No
  setup-time query can, because the message does not exist yet.

### 1.6 The decision table — and the interpretation it rests on

RFC 7489 §4.2: **DMARC passes if SPF-aligned-pass OR DKIM-aligned-pass.** Not AND. A message can
DMARC-pass with SPF failing outright.

Two consequences, in opposite directions:

- **SPF token present ⇒ ALLOW is safe.** If the SPF leg passes and aligns, DMARC passes regardless
  of DKIM, by the disjunction.
- **SPF token absent ⇏ DMARC will fail.** A sender whose alignment runs entirely through DKIM is
  legitimate and common. Blocking on absent-SPF alone re-creates §1.1's false BLOCK by a different
  mechanism.

So the build plan's locked row *"Fails, domain publishes `p=reject` or `p=quarantine` → Block"*
requires an operational definition of "fails" that is **not** "SPF evidence absent."

**M3-P4-OD-1 — RULED 2026-07-29. The user ruled their own locked rule wrong**, verbatim: *"you're
right, and my locked rule was wrong. DMARC passing on SPF-aligned **or** DKIM-aligned means DNS can
never prove failure."* The ruling has two operative parts.

**(1) The default is DOWNGRADED from BLOCK to warn-plus-explicit-acknowledgment.** Not a dismissible
toast — *"a checkbox the user must tick, not a dismissible toast — the risk is visible and knowingly
accepted."* The deciding argument is the design line: *"users control their own assets. It's their
domain, their reputation, their risk. Blocking them from their own domain on inconclusive evidence
contradicts the rule I gave you for resolving exactly this kind of ambiguity. My original reasoning
for blocking was the invisible failure — so **make the failure visible and let them choose**, which
satisfies both."*

**(2) BLOCK survives only for near-certain evidence:** *"`p=reject` **and** no SPF record **and** no
DKIM key at any common selector. That's the narrow case where 'no plausible passing path' approaches
proof."*

Note precisely what narrowed. The earlier draft's BLOCK row fired on an absent SPF *token* — i.e. a
domain with a perfectly good SPF record that merely lacked this provider's include. The ruling
requires **no SPF record at all**, which is a different and far rarer condition: a domain publishing
`p=reject` with no SPF record whatsoever and no DKIM key at any probeable selector is
misconfigured on every axis simultaneously. `p=quarantine` no longer blocks at all.

> **Definition, as ruled.** **BLOCK** = no SPF record exists for the domain **AND** no DKIM selector
> evidence exists at any probeable selector **AND** the DMARC policy is `p=reject`. Everything else
> that does not affirmatively align is **WARN + acknowledgment**.

Even BLOCK is "no plausible passing path found," not proof — so the copy says so in those terms
(§5.2), and the ruling is explicit that copy must match evidence: *"'we couldn't verify this will
authenticate,' never 'this will fail.'"*

| SPF | DKIM selector | DMARC policy | Verdict | `from_verification_state` |
|---|---|---|---|---|
| token present | any | any | **ALLOW** | `aligned` |
| token absent, record exists | any | any | **WARN + ACK** | `unaligned_warned` |
| no record at all | present | any | **WARN + ACK** | `unaligned_warned` |
| no record at all | absent / not probeable | `reject` | **BLOCK** | `blocked` |
| no record at all | absent / not probeable | `quarantine` | **WARN + ACK** | `unaligned_warned` |
| no record at all | absent / not probeable | `none` | **WARN + ACK** | `unaligned_warned` |
| no record at all | absent / not probeable | no record, or multiple records | **WARN + ACK** | `unaligned_warned` |
| provider unmatched | — | any | **WARN + ACK** | `unaligned_warned` |
| any | any | indeterminate lookup (§3) | **REFUSE, retryable** | unchanged |

**"No SPF record" and "SPF record without this provider's token" are now different verdicts, so they
must be different return values from the primitive.** §1.2's primitive currently answers a token
question; it must additionally report record-presence, and §7's tests need a case for each
(a `p=reject` domain with an SPF record lacking the token ⇒ WARN, not BLOCK — the case that would
have been a false BLOCK under the pre-ruling table).

**The acknowledgment is state, not UI.** A tickable checkbox that the backend does not record is a
UI detail the next client can skip — the same handler-vs-store-layer mistake §2.8 found. So the
acknowledgment is a required field on the write that sets a warned From, persisted alongside
`from_verification_state = 'unaligned_warned'`, and absent acknowledgment the write is **refused**.
Which columns carry it was **M3-P4-OD-10**, ruled 2026-07-30: two additive columns in **this phase's**
`0040`, not a Phase 1 amendment. See §4.6.

**The real upgrade path, recorded as future work rather than built.** The ruling names it: *"sending
a test through the actual account and parsing `Authentication-Results` is the only source of ground
truth. Phase 3's 'send a test to myself' already has the machinery."* That is the only mechanism that
can ever move a verdict from "couldn't verify" to "verified" — DNS inspection cannot, by the
disjunction at the top of this section. Registered in `Docs/future-work.md`, not in V1.

`from_spf_aligned` and `from_dmarc_aligned` record the two legs independently;
`from_check_detail` records what the resolver saw, for operator diagnosis.

**The "not probeable" collapse remains a deliberate asymmetry, but the ruling shrank its cost to
near nothing.** For SES/SendGrid/Postmark the selector is unguessable, so "no DKIM evidence" and
"cannot look for DKIM evidence" are indistinguishable, and both still land in the BLOCK row when the
domain publishes `p=reject`. Under the pre-ruling table that was the sharpest false-BLOCK risk in the
design: a SES customer with correct DKIM and no SPF *include* would have been blocked. **The ruling
removes exactly that case** — an SPF record lacking the include is now WARN, so reaching BLOCK
additionally requires the domain to publish **no SPF record at all**. A domain on `p=reject` with no
SPF record is not a plausible working SES configuration.

**M3-P4-OD-2 is therefore reduced, not discharged.** The residual case is a domain with `p=reject`, no
SPF record, and DKIM-only alignment through an unguessable selector. It is rare and misconfigured on
its face, but it is legal and it would work. Recommendation, now that BLOCK is this narrow:
**exempt unguessable-selector providers from BLOCK** — since for them "no evidence" carries no
information at all, and the ruling's own standard is that BLOCK requires evidence approaching proof.
Still registered, because it is the same class of judgment the user just ruled on and the recommended
answer changed as a result.

### 1.7 The known non-aligning configuration that a naive token match gets backwards

Gmail's "Send mail as" has two modes, and they behave oppositely:

- **Via the alias domain's own SMTP** ("treat as an alias" with a custom server): envelope, header,
  and DKIM `d=` all belong to the alias domain. Aligns trivially. The gate's normal path handles it
  correctly with no special case. **This is the legitimate send-as alias the build plan requires not
  be collateral damage** (§7, T4).
- **Via Gmail's servers** (the default): the header From is the alias, but the envelope sender and
  the DKIM `d=` are Google's/the Workspace domain's. **Neither leg aligns to the alias domain, by
  construction, even when configured perfectly.** Google documents this and recommends custom SMTP
  when the alias domain enforces DMARC.

A naive token match **false-ALLOWs** the second case: `smtp_host` is Gmail's, so `_spf.google.com`
is "the provider's token," and it is often present in the alias domain's record for unrelated
reasons — read as evidence of alignment for a domain Google will not sign for. The gate must compare
the From domain against the **authenticating** mailbox's domain, not merely find the provider's
token. Where `smtp_host` is a major provider's and the From domain shares no organizational domain
with `smtp_username`'s domain, that is the send-as-via-provider pattern and must be evaluated as
such. Microsoft's "send on behalf of" is a third case: it changes which domain the header From
actually asserts, so keying lookups off the wrong domain silently checks the wrong thing.

---

## 2. When the check runs

### 2.1 Setup only. Dispatch never resolves DNS.

Per the build plan: checked at setup, no periodic re-check. This is not merely a scheduling choice —
it is the structural guarantee behind §4. **No dispatch-path code may perform a DNS lookup.** The
dispatcher reads `from_verification_state` and nothing else. A resolver call anywhere under
`internal/dispatch/` is a Phase 4 regression, and §7 T6 asserts its absence behaviorally.

### 2.2 The four entry points — the complete bypass surface

Enumerated from the tree, not assumed. **Two handler pairs** in module 2 can write `from_address` or
`from_name` — a create and a PATCH on each of the two surfaces, four routes total once Phase 3's
user-facing pair is added alongside the operator pair it retains (§2.8). Confirmed by grep over
`db/migrations/` and `services/`:

- `sender.jobs` has **no From column** (`sender_0007:17` is `reply_to`, which is `Reply-To:`).
- `/v1/run` accepts no From: `runReq{JobItemID, UserID}` only (`platformapi/handlers.go:97-100`),
  and `createJob`'s INSERT column list (`store.go:88-89`) has no From column to write.
- `from_address`/`from_name` have **no DEFAULT**, **no trigger**, and **no backfill** in any
  migration.
- Dispatch **reads** them (`eligibility.go:166-167`) and never writes them. `applyAccountEffect`,
  `bumpAndRecompute`, `claimOne`, and the six lifecycle transitions all have From columns absent
  from their SET lists.

| # | Route | Surface | Writes From? | Gate obligation |
|---|---|---|---|---|
| 1 | `POST /v1/byosmtp/accounts` | user | yes — create | Run the gate. Persist the verdict. |
| 2 | `PATCH /v1/byosmtp/accounts/{id}` | user | yes — `from_address` and/or `from_name`, independently | **Re-run the gate whenever `from_address` is present.** §2.3. |
| 3 | `POST /v1/byosmtp/accounts/bulk` | user | yes — per row, same intake as create | Run the gate per row. §2.4. |
| 4 | `POST /v1/byosmtp/accounts/{id}/test` | user | no | Re-run the check, report the current result, update the verdict. §2.5. |
| 5 | `POST .../{id}/enable`, `/unpause` | user | no | **Must not clear a `blocked` verdict.** §2.6. |
| 6 | `POST .../{id}/test-send` (Phase 3 §3.2) | user | no — real send | Subject to the routing exclusion like any send. |
| 7 | **`POST /v1/admin/accounts`** | **operator** | **yes — create** | **§2.8. Not covered by 1–6.** |
| 8 | **`PATCH /v1/admin/accounts/{id}`** | **operator** | **yes — `from_address`, `from_name`** | **§2.8. The live bypass.** |
| 9 | Raw-SQL harness fixtures | tests | yes — bypass every handler | §2.7. |

Routes 5, 7, 8, and 9 are what the brief's watch-item is really about, and none is a "create or
update" route in the obvious sense — 5 writes no From at all, and 7/8 are on a surface this phase's
user-facing framing does not naturally reach. That is why the enumeration is by *effect on the
verdict*, not by HTTP verb or by surface.

**Correction to an earlier draft of this section.** Rows 7 and 8 were absent, and the section claimed
to be complete without them. They are the sharpest gap in the phase (§2.8), they were found by the
scoped review pass this phase's brief mandates, and the omission is recorded here rather than
silently patched: a "complete enumeration" that reasoned only over the surface a phase is *about* is
the exact shape of the mistake, and it will recur in Phase 5 (which also gates account state) unless
it is written down. Row 9's count was also wrong — three fixtures, verified as six (§2.7).

### 2.3 PATCH is the stale-verdict path, and it is the whole problem

`account_update.go:196-198` writes `from_address` after syntax validation only
(`validateFromAddress`, `account_validate.go:53-58` — the same regex as the DB CHECK, plus a 320-char
bound). The UPDATE at `:236` has no `user_id` predicate. And the audit row at `:241-243` writes
**identical** before/after flag snapshots (`fromSnap, fromSnap`), so a From change currently leaves
no diagnosable trace at all.

The rule:

- If `from_address` is present in the patch body and its value differs, the gate **re-runs
  synchronously** and the new verdict replaces the old.
- If the gate is indeterminate (§3), **the PATCH is refused** with a retryable code. The old address
  and its old verdict remain intact. This is fail-closed at setup, applied to the update path.
- If the new address BLOCKs, the PATCH **succeeds** and the account becomes `blocked` — consistent
  with Phase 3 §2.5's "failure is not a rollback," and with the fact that the user's own edit caused
  it. The response says so unmissably (§5.4); it is an actionable notification in Phase 7.
- `from_name` alone does **not** re-run the gate. It is the display name, never on the envelope, and
  no alignment property depends on it.
- The audit row's identical-snapshot defect must be fixed here for From changes specifically:
  a From change is exactly the event an operator will later need to reconstruct.

A gate wired only into create is not a gate. This is the single question the review pass in §9
answers.

### 2.4 Bulk add

Phase 3 §4's per-row response reports `status` and `verification` — credential verification only. It
is silent on the From gate, which makes bulk-add an undesigned gate entry point. Phase 4 closes it:
each row gets its own From verdict, reported per row alongside its credential verdict.

Pacing: Phase 3's `bulk_add_verify_interval_ms` paces **credential AUTH attempts per `smtp_host`**,
because the risk it manages is looking like credential stuffing. DNS lookups carry no such risk and
share nothing with it. The From check is **not** subject to that pacer; it is subject to the
per-request DNS budget (§6.4). Reusing the credential pacer for DNS would be a false economy — it
would make a 50-row bulk add wait 100 seconds for lookups that mostly hit cache.

### 2.5 The test button

Phase 3 owns the route, rate limit (`account_test_rate_limit_per_day`), and credential frames. Phase
4 adds the From check to the same request. Both verdicts are reported; both are persisted to their
own column groups. The route is the build plan's on-demand re-verification, and it is the only
mechanism by which a user can clear a stale `blocked` after fixing their DNS — which makes it
load-bearing, not a convenience. Its refusal path is Phase 3's rate limit, unchanged.

A test-button run whose DNS is indeterminate reports the indeterminacy and **leaves the verdict
untouched** (§4). It is not a failure of the button.

### 2.6 `enable` and `unpause` must not launder a block

`doTransition` (`account_lifecycle.go:141`, six thin wrappers at `:217-234`) writes only
`quarantined`/`disabled`/`paused`/`pause_reason` and structurally cannot touch From columns. That is
exactly why it is a bypass risk: if the routing exclusion were expressed as `disabled = TRUE`, then
`POST /enable` would clear a From block without any DNS re-check. This is the reason §8 rejects
reusing `disabled` and gives the exclusion its own predicate term. `unpause`'s existing
non-`manual` precondition (`:124-133`) is the in-repo precedent for a transition refusing to clear a
state it does not own.

### 2.7 Harness fixtures write From via raw SQL, and the tests must not rely on that

**Six** files insert `from_address` by raw SQL, bypassing every handler. An earlier draft said three;
`grep -rn 'INSERT INTO sender\.accounts'` over module 2's tree returns nine hits, of which six are
fixtures:

- `tests/harness/factories.go:95-99` (`'sender@example.com'`)
- `tests/harness/phase1_crypto_roles_test.go:465-474` (`'a@b.co'`)
- `services/brain/internal/dispatch/harness_test.go:298-303` (`'u@ourdomain.com'`) — the fixture every
  dispatch-package behavioral test builds accounts from
- `services/brain/internal/adminapi/health_test.go:45-53`
- `services/brain/internal/platformapi/campaign_report_test.go:36-42` — helper named
  **`seedEligibleAccount`**
- `tools/sender-migrate/runner_integration_test.go:430-434`

`seedEligibleAccount` is the one to note: its name asserts eligibility, and once §8's predicate lands
it will seed an account that is `unverified` and therefore **not** eligible. A fixture whose name
states a property it no longer has is worse than a broken one — it misdirects whoever debugs the
resulting failure.

This is not a production bypass, but it is a **test-validity** hazard of the exact class Rule 10
exists to catch: once §8 excludes non-`aligned` accounts from routing, every one of those fixtures
produces a row that **cannot send**, and every inherited dispatch test that uses them fails. The
fixtures must be updated to set a verdict explicitly, and — critically — a fixture that sets
`from_verification_state = 'aligned'` must set `from_checked_at` too or
`accounts_from_checked_pairing_check` rejects the insert. The constraint is doing useful work here:
it makes the fixture state its assumption out loud.

Two fixtures (`phase3_send.sh:69`, `load_e11.sh:85`) go through the real admin API by curl and will
see the real gate — meaning they need real DNS to succeed, which §7.1's infrastructure supplies.

### 2.8 The operator surface writes From, and it is the live bypass

**Phase 3 §1.2 keeps the entire operator surface:** *"The operator surface is NOT deleted. All twelve
`adminapi` routes remain, under Domain B, for break-glass and support"* (`phase-3-user-owned-accounts.md:221`,
restated `:1319`). Two of those twelve write the From columns, verified in the fork source:

- `POST /v1/admin/accounts` — `account_create.go:15-16`, INSERT at `:140` includes
  `from_address, from_name`.
- `PATCH /v1/admin/accounts/{id}` — `account_update.go:16-17` (`FromAddress`, `FromName` in
  `accountUpdateReq`), `set("from_address = ?", …)` at `:196-197`, `set("from_name = ?", …)` at
  `:199-200`.

Phase 2's G2 rewrites these two files only to swap `pool_id` → `user_id`; it does not touch the From
fields or the SET-list logic. So both survive into module 3 unmodified.

**The concrete bypass, and it is worse than laundering a block:**

1. Verify an account through the gated user route → `from_verification_state = 'aligned'`.
2. `PATCH /v1/admin/accounts/{id}` with a new `from_address` on a domain that would BLOCK.
3. `account_update.go`'s SET list writes `from_address` and **never mentions
   `from_verification_state`** — the column is not in `accountUpdateReq` at all.
4. The row keeps `aligned`, stays inside §8's `IN ('aligned','unaligned_warned')` predicate, and
   sends with a From that was never gated.

This preserves a *passing* verdict across an ungated address change. It is strictly more dangerous
than clearing a `blocked` one: a cleared block is at least visible as a state change, whereas this
leaves a row that reads as verified, with `from_checked_at` still pointing at a check performed on a
**different address**. Nothing in the row records that the address moved — and per §2.3 the audit row
writes identical before/after snapshots, so there is no trace anywhere.

Admin CREATE is the milder half: a new row defaults to `'unverified'`, which §8's positive list
already excludes from routing (this is the second time that positive-list choice pays for itself).
It still leaves an operator unable to create a working account without going through the user route,
which needs stating rather than discovering.

**The rule:** the gate belongs at the **store layer**, not the handler layer. Any code path that
writes `from_address` re-runs it, or writes `from_verification_state = 'unverified'` in the same
statement. A handler-level gate is what produced this gap — twice, since §2.7's fixtures are the
same mistake in test code.

**M3-P4-OD-9 — RULED 2026-07-29**, verbatim: *"the gate applies at the store layer to everything,
operator routes included. If break-glass genuinely needs to set an unverified From, that's an explicit
audited override flag, not a side door. Same pattern as break-glass credential read: deliberate and
logged, never accidental."*

This **supersedes the three options above.** The earlier recommendation (b) — silently resetting to
`'unverified'` in the same UPDATE — is *not* what was ruled, and the difference matters: a silent
reset is a side door that happens to fail closed. The ruling requires the operator to *say* they are
bypassing the gate.

So, precisely:

1. **The gate runs at the store layer, for every writer of `from_address`, operator routes included.**
   No route is exempt by virtue of being Domain B. Verified writers today: `account_create.go:140`,
   `account_update.go:196-200`, plus §2.7's harness fixtures, which are the same mistake in test code.
2. **The one way past it is an explicit override flag on the request** — a named field the caller sets
   deliberately, not a default and not an omission. Absent the flag, an operator write with a From that
   does not pass is **refused**, exactly as a user write is.
3. **The override writes `from_verification_state = 'unverified'`**, never `aligned` and never
   `unaligned_warned`. §8's positive-list predicate then excludes the account from routing until
   someone presses Test. The override buys the operator the ability to *store* the address, not the
   ability to *send* from it — which is the honest scope of a repair action.
4. **The override is audited as its own event**, with the actor, the address, the reason, and the
   verdict that was overridden. This is the *"same pattern as break-glass credential read: deliberate
   and logged"* half of the ruling, and it is why (b) was insufficient — (b) produced no record that a
   human chose to bypass a gate.
5. **A reason is required**, matching the break-glass credential read's contract (build plan, locked:
   *"time-limited, reason-required, audited"*). An override with no reason is refused.

**What this does not do:** it does not make the gate depend on DNS at 3am in the way option (a) would.
Option (a)'s objection was that a break-glass route would be *blocked* by broken DNS; under the ruling
an indeterminate lookup is still §3's retryable refusal for a normal write, but the override path is
available immediately and does not resolve DNS at all. The operator's escape hatch is the flag, not a
lookup that might not answer.

T8 covers all of it: the admin PATCH must not leave an `aligned` verdict attached to an address the
gate never saw; an operator write without the flag on a non-passing From is refused; an operator write
**with** the flag lands `'unverified'` **and** writes the audit event; and the account does not route
until re-verified. The paired control is an operator write with the flag on a From that **would** pass —
it should not be quietly downgraded to `'unverified'` just because the flag was present.

---

## 3. Failure modes of the check itself

**Default posture: FAIL CLOSED at setup.** The user retries. But "fail closed" must be applied to
the right cases, and the distinction that decides it is *absence versus ignorance*.

### 3.1 The distinction the RFC does not make and the gate must

RFC 7489 speaks only of an "empty resultant set." It was written assuming working DNS and does not
address SERVFAIL or timeout. Filling that silence is unavoidable, and getting it backwards is
dangerous in a specific way:

> **Treating a resolver timeout as "no DMARC record" is an availability-triggered downgrade.** A
> flaky resolver — or an attacker who can degrade one — silently converts every BLOCK into an ALLOW.

So: **NXDOMAIN and NODATA are genuine absence** (a real, actionable result — proceed to the
no-DMARC row of §1.6). **SERVFAIL and timeout are ignorance** (not a result — refuse, retryable).

Go's stdlib makes this awkward and the awkwardness must be handled explicitly:
`net.DNSError.IsNotFound` is set for **both** NXDOMAIN and NODATA — which is fine, since the gate
treats them identically — but the rcode is not exposed at all. The load-bearing distinction is
carried by `IsTemporary` and `IsTimeout`, and those two fields are what the implementation must
branch on. A gate that branches on `IsNotFound` alone silently classifies SERVFAIL as absence: the
downgrade above, delivered by a one-line mistake.

### 3.2 The full table

| Case | Classification | Gate behavior |
|---|---|---|
| DNS timeout | ignorance | REFUSE, retryable — `from_dns_unavailable` |
| SERVFAIL | ignorance | REFUSE, retryable — `from_dns_unavailable` |
| NXDOMAIN at `_dmarc.<domain>` | absence | real result → org-domain fallback, then no-DMARC row |
| NODATA at `_dmarc.<domain>` | absence | same as NXDOMAIN |
| No SPF record at all (including a From domain that does not exist) | absence | SPF token absent → §1.6. **M3-P4-OD-13, ruled 2026-08-01:** an earlier draft of this row gave NXDOMAIN on the From domain itself a DECISIVE, distinct treatment (`from_domain_not_found`, 400, "not a warn — a typo the user can fix"). Dropped: `net.DNSError.IsNotFound` is set identically for NXDOMAIN and NODATA with no way to tell them apart through the stdlib resolver API (verified empirically against the real fixture server — a legitimate SPF-publishing domain with no A/AAAA record reports the SAME flag a nonexistent domain does), so a domain that does not exist now falls through this SAME absence row, landing at an ordinary WARN — never a distinct code, and never a false BLOCK (every other lookup independently also reports absence for a nonexistent domain, so BLOCK's own three-condition conjunction still cannot fire from this alone). |
| Malformed SPF (unparseable) | PermError | Surface as **the domain's own record being broken**, `from_spf_record_invalid` — distinct copy, because the fix is unrelated to provider choice (§5.5) |
| Multiple `v=spf1` records | PermError (RFC 7208 §4.5) | `from_spf_multiple_records` — actionable, names the defect |
| SPF exceeds 10 lookups | PermError (§4.6.4) | Governed by `from_spf_lookup_limit_posture` (§6.3) |
| SPF exceeds 2 void lookups | PermError (§4.6.4) | Same posture dial |
| Multiple `v=DMARC1` records | non-detection (§1.3) | Treat as **no enforceable policy** → warn row, never block. `from_dmarc_ambiguous` in the detail. |
| SPF record contains macros (`%{i}`, `%{h}`) | unevaluable at setup | **Never block.** Macros expand from a live SMTP transaction that does not exist at setup; any value we synthesize is fabricated. Warn, and say we could not fully verify. |
| `exp=` present | irrelevant | Ignored. It is a human-readable explanation string for a receiver-side fail, not an input to any decision. |
| TempError from a sub-lookup | ignorance | REFUSE, retryable — do **not** cache, do not treat as a result |

### 3.3 Retry is the user's, not a background loop's

"Retryable" means the response tells the user to try again, and the test button is the retry
mechanism. Phase 4 adds **no** background retry loop, no queue, and no timer. Reasons: a background
re-check is the periodic re-check the build plan explicitly excludes; and a retry loop that writes
verdicts is a second writer of `from_verification_state` that could re-block a verified From
mid-campaign (§4) — the one forbidden behavior.

---

## 4. The mid-campaign rule

**Locked:** a previously verified From is never re-blocked by a transient lookup failure. This
section states the mechanism, because the guarantee has to be structural rather than a promise.

### 4.1 Three properties, each independently sufficient to be worth stating

1. **Dispatch never resolves DNS** (§2.1). A DNS outage cannot reach the send path, because the send
   path has no resolver. There is no timeout to fail, no cache to miss, no code to degrade. This is
   the primary guarantee, and it is structural in the same sense as the box having no DB driver.
2. **Only the four setup-time entry points write `from_verification_state`.** No loop, no sweep, no
   trigger, no dispatch-path code. §2.2 enumerates them exhaustively.
3. **An indeterminate lookup writes only the attempt columns.** This is what Phase 1's two-timestamp
   split bought:

| Outcome | `from_verification_state` | `from_checked_at` | `from_last_attempt_at` | `from_last_attempt_error` |
|---|---|---|---|---|
| Check succeeds | **written** | **written** (= now) | written | cleared |
| Check indeterminate | **untouched** | **untouched** | written | written |

A DNS blip therefore leaves a full diagnostic trail (`from_last_attempt_*` shows what failed and
when) while the verdict and its timestamp are byte-unchanged. `accounts_from_checked_pairing_check`
enforces the pairing, so a code path that tried to write a state without a timestamp would be
rejected by the database rather than silently producing an unexplainable row.

### 4.2 The two ways this could still be got wrong

- **A verdict reset on PATCH.** §2.3 refuses an indeterminate PATCH rather than resetting the
  verdict to `unverified`. Resetting would be re-blocking through the back door — the account stops
  sending because a lookup failed, which is exactly the forbidden outcome, arrived at by a path that
  does not look like re-blocking.
- **A cached ALLOW expiring into a block.** The cache (§6.5) is a *lookup* cache, not a *verdict*
  cache. Verdicts live in the database and never expire. A cold cache means the next setup-time
  check is slower, never that a stored verdict changes.

### 4.3 The accepted cost, stated plainly

A From verified once stays verified until the user next PATCHes it or presses the test button. If the
domain owner later tightens DMARC to `p=reject`, or removes the provider's SPF include, **we will
keep sending and the mail will be silently discarded** — the exact invisible failure this gate
exists to prevent, arriving through the door the never-re-block rule holds open.

This is the deliberate trade the build plan makes: an outage that halts a legitimate campaign is
judged worse than a stale verdict on a domain the user themselves broke. It is recorded here as a
known cost rather than left implicit, and it is the strongest argument for the periodic re-check the
build plan defers. Registered as future work, not reopened here.

### 4.6 The acknowledgment columns (M3-P4-OD-10, ruled 2026-07-30)

**Two additive columns on `byosmtp.accounts`, added by this phase's own `0040`.** Phase 1 is not
amended. The ruling's reasoning, recorded because it generalizes past this phase: *"Phase 1 locked the
schema as understood then. My Phase 4 ruling created a requirement it couldn't have anticipated, and
an additive column in the discovering phase's own migration is exactly what additive-only is for."*

The precedent is exact and was verified in module 2's tree rather than recalled: `sender_0016`
(`db/migrations/sender_0016_accounts_scoring_columns.up.sql`) added `consecutive_failures` and
`throttle_paused_at` to the *already-locked* `sender.accounts` **for Phase 5**, with its header stating
the same three things this migration must state — additive-only (no existing column, constraint, or
enum touched), no new `GRANT` required (table-level DML already covers every column, so
`TestGrantsInvariant` stays green by construction), and the metadata-only/back-fill note.

```sql
ALTER TABLE byosmtp.accounts
    ADD COLUMN from_warning_acknowledged_at  TIMESTAMPTZ,   -- NULL = not acknowledged
    ADD COLUMN from_warning_acknowledged_for  TEXT;         -- the verdict fingerprint ticked against
```

**Why two columns and not a boolean.** §5.3.1 requires re-acknowledgment when the **verdict changes**
but explicitly *not* when the check merely re-runs. A boolean cannot distinguish those two cases, so
T13c's control (a) and its positive would be indistinguishable — the test would pass on an
implementation that never invalidates anything. The second column stores what the user was actually
shown, so the predicate can ask *"is the stored acknowledgment still for the verdict currently in
force?"* rather than *"did they ever tick anything?"* A `p=none` acknowledgment silently carried across
a change to `p=reject` is consent to something the user never saw.

**Why a timestamp rather than a boolean for the first column.** It costs nothing over a boolean,
matches `throttle_paused_at`'s inherited NULL-means-not-set idiom, and makes the support question
*"when did they accept this?"* answerable. `NOT NULL` is not available: existing rows and every
`aligned` row have no acknowledgment, which is the correct neutral state.

**What `from_warning_acknowledged_for` holds** is the *verdict fingerprint*, not free text — the
deciding inputs to §1.6's table. It is compared only for equality against the in-force value, never
parsed, never shown to a user, and never crosses the client boundary. A `CHECK` bounding its length
mirrors `accounts_from_attempt_error_len_check` (Phase 1 §4.4) rather than inventing a new bound.

**What it is compared *against* is the load-bearing part**, and there are two ways to supply it. Phase 1
§4.4 stores the verdict *inputs* (`from_dmarc_policy`, `from_spf_aligned`, `from_dmarc_aligned`, with the
resolver's raw observation in `from_check_detail`) but no fingerprint, so one of these is required:

| | How the in-force value is obtained | Cost |
|---|---|---|
| **(a) A generated column** — `from_verdict_fingerprint … GENERATED ALWAYS AS (<expr over the Phase 1 verdict columns>) STORED` | Postgres recomputes it on every write that touches a verdict column, so a re-derived verdict invalidates a stale acknowledgment **by data**. | The expression must be immutable, and it encodes §1.6's table in SQL — a second home for the verdict logic. |
| **(b) A plain column the write path sets** alongside the verdict | No expression in the schema; the fingerprint is computed once, in Go, where §1.6's table already lives. | It is an invalidation the write path can forget — and forgetting it fails **open** (a stale acknowledgment keeps sending). |

**(a) is recommended**, for the reason the ruling itself turns on: (b)'s failure mode is silent and
permissive, and it re-creates in miniature the handler-vs-store-layer gap §2.8 found. (a) cannot be
forgotten, because there is no statement to forget. If (a) proves impractical (the immutability
requirement is the likely blocker), (b) is acceptable **only** with T13c extended to cover the
verdict-change path through *every* writer that re-derives a verdict — §2.3's PATCH and the test
button — since that is precisely the enumeration (b) relies on being complete.

This is an implementation choice, not a further Open Decision: both options satisfy §8(c) identically,
neither changes the two columns above, and the predicate is written against the name in either case.

**No new constraint is required.** Phase 1 §4.4 already notes that
`accounts_from_checked_pairing_check` constrains only the
`from_verification_state`/`from_checked_at` pair, so it accommodates these columns unchanged. A
pairing constraint tying the two new columns together is **not** added: §8(c)'s predicate consults
them jointly, and a constraint would convert an ordinary invalidation race into a write error — the
mistake Phase 5 §7's `23502` finding already paid for once.

**Grants:** none. Both columns land on `byosmtp.accounts`, which `byosmtp_module` already holds
table-level DML on. Stated explicitly because ADR-0008's trap is a *new table* needing a grant, and
the distinction is what keeps `TestGrantsInvariant` from being edited to accommodate a migration.

---

## 5. What the user sees

Rule 5's two-vocabulary rule applies: internal reasons never cross the boundary. Following Phase 3
§2.3's precedent, this phase publishes its **own** external code vocabulary rather than reusing
`error_class` (whose six values are far too coarse — they cannot distinguish a broken SPF record from
a policy block) and rather than serializing internal resolver detail.

### 5.1 New external codes

| Code | HTTP | Meaning |
|---|---|---|
| `from_alignment_blocked` | `201` body field (not an error envelope — the row is created) | The BLOCK verdict |
| `from_dns_unavailable` | `503` | Indeterminate lookup; retry |
| `from_spf_record_invalid` | `201` body field | The domain's own SPF record is malformed |
| `from_spf_multiple_records` | `201` body field | Two or more `v=spf1` records |
| `from_spf_lookup_limit_exceeded` | `201` body field | Per §6.3's posture |
| `from_unauthenticated_warned` | `201` body field | The WARNING verdict |

`from_domain_not_found` is **not** in this table — an earlier draft included it for the NXDOMAIN-
on-the-From-domain case §3.2 originally specified as decisive. **M3-P4-OD-13 (ruled 2026-08-01)
removed that case**: it cannot be reliably distinguished from an ordinary NODATA absence through
Go's stdlib resolver, so it is never emitted; a nonexistent From domain surfaces as
`from_unauthenticated_warned` like any other absence.

No new `error_class` value; that enum is additive-only and untouched.

`from_check_detail` (JSONB) holds what the resolver saw, for operator diagnosis. It is **not**
returned to the user verbatim: it may contain third-party DNS content, and a JSONB blob is one
`SELECT` away from a response body. The user-facing copy is assembled from the verdict and the named
failing record, never by dumping the detail.

### 5.2 BLOCK copy — three things, and an honest modality

The build plan requires the block to say which record failed, what the policy is, and what to fix.
Per §1.6 the gate found *no passing path*, not *proof of failure*, and the copy must not overclaim.
The ruling makes this a copy **contract**, not a style preference: *"'we couldn't verify this will
authenticate,' never 'this will fail.'"*

This copy now fires only in the narrow ruled case — `p=reject`, **no SPF record at all**, no DKIM at
any probeable selector — so it names all three:

> **We couldn't verify that `alice@example.com` will authenticate from this account.**
> `example.com` publishes a DMARC policy of `p=reject`, which asks receiving mail servers to reject
> messages that aren't authenticated as coming from `example.com`.
> We found no authentication path at all: `example.com` publishes **no SPF record**, and we found no
> DKIM key at `google._domainkey.example.com`. With neither, we can't identify any way for a message
> to authenticate.
> **To fix:** publish an SPF record for `example.com` including `_spf.google.com` (Google Workspace,
> which is what `smtp.gmail.com` is), or set up DKIM signing for `example.com` in your Google
> Workspace admin console. Then press **Test** to re-check.

Named: the records (both absent), the policy (`p=reject`), the fix, and the retry mechanism. The
headline is "we couldn't verify" — not "your mail will be rejected", which is a claim about the future
the gate cannot make, and not "we can't send as this address", which the pre-ruling draft said and
which now overstates a verdict the user can no longer reach by accident.

**Two words are banned from this copy and its variants, and a test asserts their absence** (§7): *will
fail* and *will be rejected*. The check is mechanical because the failure mode is drift — copy is the
artifact most likely to be reworded by someone who has not read this section, and the ruling's
distinction is invisible to a proofreader.

### 5.3 WARNING copy — what will probably happen, and why it is not ideal

Post-ruling this is the **default** path for everything that does not affirmatively align, so it now
carries the acknowledgment (§5.3.1) and covers cases that previously blocked.

> **This will probably send, but it won't be authenticated.**
> `example.com` doesn't publish a DMARC policy, so receiving servers are unlikely to reject your
> messages outright. But `example.com`'s SPF record doesn't authorize this account either, so your
> mail will arrive unauthenticated — more likely to land in spam, and it builds no sending
> reputation for your domain over time.
> **To fix (recommended):** add `include:_spf.google.com` to `example.com`'s SPF record.

The unmatched-provider variant states the limit of our knowledge rather than implying a defect:

> We couldn't identify `mail.mycompany.net` as a known mail provider, so we can't verify whether
> `example.com` authorizes it. If this is your own mail server and `example.com`'s SPF record covers
> it, you're fine.

**The enforcing-policy variant is new, and it is the one the ruling created.** A `p=reject` or
`p=quarantine` domain that has *some* authentication evidence — or an SPF record merely lacking this
provider's include — now warns instead of blocking. This is the highest-risk warned case, so it says
so plainly while still not overclaiming:

> **We couldn't verify that `alice@example.com` will authenticate from this account.**
> `example.com` publishes a DMARC policy of `p=reject`, which asks receiving servers to reject
> messages that aren't authenticated as coming from `example.com`. Its SPF record doesn't include
> `_spf.google.com`, so we can't confirm this account is authorized.
> DMARC can also pass via DKIM, which we can't check before a message is sent — so if DKIM signing is
> set up for `example.com`, your mail may authenticate normally.
> **If it doesn't, a `p=reject` policy means your messages are likely to be rejected outright, not
> just filtered.** This is your domain and your decision.
> **Recommended:** add `include:_spf.google.com` to `example.com`'s SPF record first.

That last paragraph is deliberately the strongest language in the phase, and it is still conditional.
It is the honest form of the risk the pre-ruling BLOCK was protecting against — which is the whole
point of *"make the failure visible and let them choose."*

### 5.3.1 The acknowledgment — a tick, not a toast

The ruling: *"A checkbox the user must tick, not a dismissible toast — the risk is visible and
knowingly accepted."*

- **The write is refused without it.** Setting a From that resolves to `unaligned_warned` requires an
  explicit acknowledgment field on the request; absent it, the account is created/updated with the
  From **unset** rather than warned-and-accepted. Silence is never consent.
- **It is enforced at the store layer, per M3-P4-OD-9's ruling** — the same place §8's positive-list
  predicate lives. A handler-layer acknowledgment check is bypassable by the next handler, which is
  exactly the finding of §2.8.
- **It is recorded, so it is auditable.** Support's question is *"did the user see the `p=reject`
  warning and accept it?"*, and that must be answerable from the row, not inferred from a client
  build. The acknowledgment is stored with the verdict and its timestamp.
- **Re-acknowledgment is required when the verdict changes**, not when it merely re-runs. A user who
  accepted a `p=none` warning has not accepted a `p=reject` warning; §2.3's PATCH path and the test
  button both re-derive the verdict, so a verdict change must invalidate the prior acknowledgment.
  A stale acknowledgment carried across a policy change is consent to something the user never saw.
- **It is not a dial.** There is no config key to disable the acknowledgment requirement; the only
  kill switch is `from_gate_mode` (§6.2), which disables the whole gate.

The columns carrying it are `from_warning_acknowledged_at` and `from_warning_acknowledged_for`, added
by this phase's own `0040` — **M3-P4-OD-10, ruled 2026-07-30, specified in §4.6.**

### 5.4 The PATCH-caused block is a distinct message

A block on create is a setup obstacle. A block on PATCH **stops a working account**, and the copy
must lead with that consequence rather than with the DNS explanation:

> **Warning: this account has stopped sending.** You changed its From address to
> `alice@newdomain.com`, and `newdomain.com`'s DMARC policy blocks unauthenticated mail from this
> account. Queued messages will wait. Change the From address back, or fix `newdomain.com`'s SPF
> record and press **Test**.

"Queued messages will wait" is a factual statement about the routing exclusion (§8), not
reassurance — jobs stay `queued`, nothing fails.

### 5.5 The domain's-own-record-is-broken case

Folding a malformed SPF record into generic block/warn copy would send the user to fix the wrong
thing:

> **`example.com`'s SPF record has an error.** It publishes two separate `v=spf1` records; the SPF
> standard allows only one, and receiving servers treat this as a permanent error — which means SPF
> authentication fails for *all* your mail from `example.com`, whatever provider you send through.
> **To fix:** merge them into a single `v=spf1` record.

---

## 6. Config dials

Rule 1: every tunable is a seeded row with a documented `default_value` and `value_type`, JSONB
`{"v": …}`, with an entry in the config reference **in the same change** and a real consumer at the
seeding commit.

### 6.1 The table

| Key | Default | Type | Consumer at the seeding commit |
|---|---|---|---|
| `from_gate_mode` | `{"v": "enforce"}` | string | §6.2, the verdict applier |
| `from_dns_resolver` | `{"v": ""}` | string | §6.6, the resolver constructor |
| `from_dns_lookup_timeout_ms` | `{"v": 3000}` | integer | §6.4, per-lookup deadline |
| `from_dns_total_budget_ms` | `{"v": 15000}` | integer | §6.4, whole-check deadline |
| `from_dns_cache_ttl_sec` | `{"v": 3600}` | integer | §6.5, positive-result cache |
| `from_dns_negative_cache_ttl_sec` | `{"v": 300}` | integer | §6.5, absence cache |
| `from_spf_lookup_limit_posture` | `{"v": "warn"}` | string | §6.3, the PermError branch |

Seven keys. The brief named four (resolver, timeout, cache TTL, SPF lookup-limit posture); the other
three are **additions, flagged here rather than smuggled in**: `from_gate_mode` (§6.2),
`from_dns_total_budget_ms` (§6.4), and `from_dns_negative_cache_ttl_sec` (§6.5). Each exists because
a specific failure has no other expression.

Every key has a real consumer at its seeding commit. **None is RESERVED** — the inherited tree has
two accidental "seeded but never consumed" instances (`retry_count_4xx`,
`variation_zero_width_rate_pct`), both found by accident in a Phase 9.5 sweep, which is the defect
class the rule exists to close.

**Re-checked against M3-P4-OD-1's ruling: still seven keys, and no key is orphaned.** The check matters
because the ruling deleted a decision branch, and a deleted branch is the standard way a key becomes
seeded-but-unconsumed — it is exactly how Phase 5 §4's ruling orphaned `quarantine_threshold`
(M3-P5-OD-1). Per key: the five DNS keys (resolver, timeout, total budget, two cache TTLs) are inputs
to the lookup and are untouched by a change in how the *verdict* is computed. `from_gate_mode` keeps a
narrower but real consumer (§6.2 above). `from_spf_lookup_limit_posture` is the one to look at twice,
and it survives: its `block` posture now means "treat a PermError as blocking **even though an SPF
record exists**," which is a deliberate operator override of the ruling's own condition rather than a
contradiction of it — the ruling's default is `warn`, which is what this key already ships as. **No new
key is added.** Notably the acknowledgment is *not* a dial: §5.3.1 states it is a contract, and making
it configurable would let an operator switch off the thing the ruling's permissive default rests on.

### 6.2 `from_gate_mode` — the kill switch, and why a gate needs one

`off | warn_only | enforce`, default `enforce`. Copying `autoquarantine_mode`'s shape exactly:
validated at config load via `validateEnumString` (`config_validate.go:178-184`), and the consumer
**degrades an unrecognized value to the safe default with a log line** rather than failing
(`account_effect.go:70-75`).

Why it exists: the gate's BLOCK branch depends on a curated provider table (§1.4) that will drift.
When a provider changes its SPF token, the failure is a **false BLOCK on every customer of that
provider** — and without a dial, the only remedy is a code deploy. `warn_only` downgrades every
block to a warning; `off` skips DNS entirely.

**M3-P4-OD-1's ruling narrows this dial's blast radius without retiring it, and re-points it.**
Post-ruling, BLOCK requires no SPF record *at all* — so the specific drift scenario above (a provider
changes its token, and a record that *is* published stops matching) no longer produces a false BLOCK;
it produces a false *warning*. That is a real de-risking, and it is why the ruling is the better
default rather than merely the kinder one. Two things follow, both worth stating because the naive
reading is that the dial is now dead:

- **The dial still has a job**, and it is now mostly `off`. `off` is the only escape from a resolver-side
  incident: if DNS is broken in a way §3.1's fail-closed path turns into a wall of `503`s at setup,
  `off` is what lets users onboard while it is fixed. That is not covered by the ruling.
- **`warn_only`'s remaining job is the DKIM probe** (§1.5), which is now a BLOCK *condition* and is the
  least reliable input in the conjunction — selector conventions are guessed, not published. A probe
  that starts failing wrongly makes "no DKIM anywhere" true for a whole provider, and since the other
  two conditions are cheap facts, that single wrong input is enough to BLOCK. `warn_only` is the
  no-deploy remedy, and T12 is written against the three-condition zone precisely so this path is
  actually tested.

The default stays `enforce`, for the reason below.

Default `enforce`, not `shadow`, and the divergence from `autoquarantine_mode` is deliberate:
autoquarantine acts destructively on its own initiative against a running account, so it earns a
cautious default. This gate runs synchronously with a user present, at setup, and refuses a
configuration the user can immediately fix. The build plan calls it *"the only defence."* Shipping it
off by default would ship the risk it exists to remove.

Note `off` and `warn_only` are **not** Rule 7 subsystem switches — the gate is not autonomous. It is
an operational escape hatch for table drift, and it is documented as such.

### 6.3 `from_spf_lookup_limit_posture` — the one place a PermError is ambiguous

`warn | block`, default `warn`.

A From domain whose SPF chain exceeds 10 lookups (or 2 void lookups) is a real PermError: receiving
servers will fail SPF for it. So `block` is defensible when the domain publishes `p=reject`. But the
limit is also the single most common misconfiguration in real SPF records, it is often caused by an
`include:` the user does not control, and blocking on it hits domains whose DKIM may be perfectly
fine (§1.6's disjunction again). Default `warn`; the dial exists because operators of stricter
deployments may reasonably disagree.

A mode string rather than a boolean, matching `autoquarantine_mode`'s precedent, so a third posture
can be added additively later.

### 6.4 The two timeouts, and the ordering invariant between them

A per-lookup timeout alone is not enough, and this is the failure that would wedge the API. The
worst case for one check is 1 DMARC + 1 org-domain DMARC + 1 SPF + up to 10 SPF sub-lookups + up to
3 DKIM probes ≈ 16 sequential lookups. At a 5s per-lookup timeout that is **80 seconds inside one
HTTP request** — past any reverse proxy's patience, and the user sees a dead connection rather than
a verdict.

So the whole check carries a single deadline, `from_dns_total_budget_ms`, and exceeding it is
**ignorance, not absence**: `from_dns_unavailable`, retryable, verdict untouched (§3.2).

**New ordering invariant, enforced at config load, not documented and hoped for** (Rule 1's
`config_ordering_violation` posture):

```
from_dns_lookup_timeout_ms  <=  from_dns_total_budget_ms
```

A config set violating it is REJECTED at load with `config_ordering_violation` — not clamped. A
budget below a single lookup's timeout makes every check fail with an indeterminate result, which
presents as "account creation is broken" with no diagnosable cause. This is the same class as the
Phase 8 HIGH finding where a routine admin config write could wedge the Brain (CLAUDE.md Rule 11),
so it is enforced rather than trusted.

There is also a **near-invariant** worth recording so a later phase does not rediscover it:
`from_dns_total_budget_ms` × bulk-add batch size must stay under the HTTP timeout in front of the
module. Not enforceable at config load — the HTTP timeout is not a module config key — and mitigated
in practice because bulk rows for one domain share cache entries. Recorded as a documented
operational bound, following Phase 3 §7's precedent for the same shape of problem.

### 6.5 Cache TTLs — asymmetric, and the asymmetry is the point

Two dials, not one, because the two directions of staleness have opposite costs.

- **A stale positive** ("the token is present," "this domain publishes `p=reject`") that has since
  changed is inconvenient.
- **A stale negative** ("no DMARC record here") is a cached decision to be *more permissive*. If the
  domain publishes DMARC after we cached its absence, we keep allowing what the owner now asks
  receivers to reject — for as long as the negative TTL runs. **Leniency-by-caching is the dangerous
  direction.**

Hence `from_dns_negative_cache_ttl_sec` (300s) well below `from_dns_cache_ttl_sec` (3600s), and a
second ordering invariant:

```
from_dns_negative_cache_ttl_sec  <=  from_dns_cache_ttl_sec
```

Also enforced at load. A negative TTL exceeding the positive one silently inverts the safety
asymmetry — the failure would be invisible, which is precisely why it is checked rather than
described.

**Cache miss at setup: just do the lookup.** Setup is user-initiated, rare, and already
synchronous. There is no warm-up, no pre-fetch, no stale-while-revalidate. A miss costs latency
bounded by §6.4, never correctness.

**Where the cache lives:** in-memory in the Brain process, which is a singleton under an advisory
lock (Rule 7), so there is exactly one cache and no coherence problem. Not a DB table — that would
need a migration, grants (ADR-0008), and a sweep loop, to cache data whose authoritative copy is
public DNS. A restart empties it; the cost is latency on the next setup, and no verdict changes
(§4.2).

**TTL is ours, not DNS's, and that is a compromise not a choice.** `net.Resolver` does not expose
record TTLs at all. Honoring real TTLs would require a lower-level client (`miekg/dns`). So the cache
lifetime is decoupled from actual DNS TTLs, and the defaults are deliberately short — below typical
SPF/DMARC TTL norms — because we cannot see the real value and must not overstay a stale answer.
Registered as **M3-P4-OD-5**.

### 6.6 `from_dns_resolver`

Empty string = the system resolver. A non-empty value is a `host:port` the gate dials explicitly.

Two consumers, and the second is why it must be a dial and not an env var: the behavioral tests in
§7 point it at a local authoritative server in compose. A test-only mechanism for this would mean
the tested code path differs from the production one — a Rule 3 violation. Validated at config load
as either empty or a parseable `host:port`.

---

## 7. Behavioral tests

Rule 3: real behavior against real infrastructure. Rule 10: a test that cannot fail is not a test;
guard tests carry paired positive/negative controls.

### 7.1 Infrastructure — real DNS, and what "domains you control" has to mean

The brief specifies real DNS against domains we control. Taken literally — registered public domains
with published records — that fails three of the eight tests below: a public domain cannot be made
to SERVFAIL on demand, cannot be made to time out, and cannot have its records changed within a test
run (TTLs, propagation).

So: a **local authoritative DNS server** in compose, alongside `postgres`/`brain`/`box`/`smtp-sink`,
serving zone files this repo owns, with `from_dns_resolver` pointed at it. That is real DNS —
real protocol, real resolver, real `net.Resolver` code path, the production evaluator unmodified —
and it is a domain we control in the strongest available sense: we control its answers, including
its failures.

What this does not exercise: real-internet recursive resolution, and real records drifting under us.
Mitigated by a small number of read-only assertions against stable public domains (the §1.1
`smtp.gmail.com` / `_spf.google.com` facts), marked as **canary** tests — allowed to fail loudly
when Google changes something, because that is exactly the signal the §1.4 table needs. Registered
as **M3-P4-OD-6**: canaries in CI make CI depend on the public internet.

Zone fixtures needed: aligned domain; misaligned + `p=reject`; misaligned + `p=quarantine`;
misaligned + `p=none`; no DMARC; two DMARC records; two SPF records; malformed SPF; >10-lookup SPF
chain; macro-bearing SPF; send-as alias domain; a SERVFAIL zone; a black-hole name for timeouts.

**Two fixtures added by M3-P4-OD-1's ruling**, and they are the ones the whole BLOCK condition now
turns on, so they must be distinct zones rather than variants of one:

- **`p=reject` + an SPF record that does not include this provider** — the case that used to BLOCK and
  now WARNs. This is the SES/SendGrid customer, and it is the majority case.
- **`p=reject` + NO SPF record published at all** (no `TXT` beginning `v=spf1`) + no DKIM at any
  common selector — the *only* remaining BLOCK.

A single "misaligned + `p=reject`" fixture cannot express both, and a test suite that carries only the
first will show BLOCK as dead code; one that carries only the second will pass while the far more
common WARN path is untested. The pair is the control for the ruling itself.

### 7.2 The tests

| # | Test | Positive assertion | Paired control that must fail if the property breaks |
|---|---|---|---|
| T1 | Aligned From → allowed | `from_verification_state='aligned'`, `from_spf_aligned=TRUE`, `from_checked_at` set, account routable | **Same From, misaligned zone** → not `aligned`. Same account, same code path, one zone difference. |
| T2 | **`p=reject` + no SPF record at all + no DKIM → BLOCKED** *(rewritten per M3-P4-OD-1)* | `state='blocked'`, `from_dmarc_policy='reject'`, body carries `from_alignment_blocked` **and names both absent records** — assert the SPF *and* DKIM substrings, not just the code | **The ruling, as a control:** the same From on the **`p=reject` + SPF-record-present-but-no-include** zone → `unaligned_warned`, **not** `blocked`. This is the majority case and the exact false-BLOCK the ruling eliminated. Second control: `p=quarantine` + no SPF record → also `unaligned_warned`, since BLOCK now requires `p=reject` specifically. |
| T2b | **BLOCK requires all three conditions — each one alone is not enough** | Three separate zones, each satisfying exactly two of {`p=reject`, no SPF record, no DKIM at any common selector} → all three produce `unaligned_warned` | **The positive is T2 itself** (all three → `blocked`). Without this test the implementation can drop a condition from the conjunction and still pass T2. The `p=reject` + no-SPF + **DKIM-present** row is the one most likely to regress, because DKIM probing is the expensive check an implementation is tempted to skip. |
| T3 | Misaligned + `p=none` → allowed WITH warning | `state='unaligned_warned'` **and** the warning is present in the body (`from_unauthenticated_warned`) | **Assert on the warning, not the allow.** An allow with no warning passes a naive test. The control: the aligned zone produces `aligned` with **no** warning field — so the test distinguishes "allowed" from "allowed and warned." |
| T3b | **Warn copy never claims a failure** (§5.2, §5.3) | Assert the response body and the stored warning text for **every** warn/block verdict contain the hedged form (`"couldn't verify"`) and, by substring assertion, do **not** contain `"will fail"`, `"will be rejected"`, `"will bounce"`, or `"is misconfigured"` | The control is the assertion's own direction: seed a deliberately wrong copy string containing `"will fail"` in a test fixture and assert the check **catches** it. A banned-phrase test that has never been shown to fire is a spell-check that matches nothing. This is the test §5.2 requires by name. |
| T3c | **Enforcing-policy warning is distinguishable from the ordinary one** (§5.3.1) | `p=reject` + SPF-record-present-but-no-include → `unaligned_warned` **and** the body carries the stronger enforcing-policy variant, naming the observed policy | Control: `p=none` misaligned → `unaligned_warned` with the **ordinary** variant. Same state, different copy — so the test proves the severity tracks the observed DMARC policy rather than being cosmetic. Both must still pass T3b's banned-phrase assertion, which is what keeps "stronger" from becoming "claims a failure." |
| T4 | Send-as alias, correctly configured → allowed | The alias-domain-own-SMTP case (§1.7) → `aligned`. The legitimate case is not collateral damage. | The **via-provider** alias case (§1.7's second mode) → **not** `aligned`. Same alias address; the difference is which infrastructure sends. This is the false-ALLOW a naive token match produces. |
| T5 | DNS timeout at setup → fails closed, retryable | Black-hole zone → `503 from_dns_unavailable`; `from_last_attempt_at`/`_error` written; `from_verification_state` and `from_checked_at` **untouched** | The **SERVFAIL** zone must behave identically, and the **NXDOMAIN** zone must **not** — NXDOMAIN is a real result. This is the §3.1 distinction, asserted in both directions. |
| T6 | Transient failure does not disable a verified From | Verify a From (`aligned`), then induce resolver failure, then **dispatch a real job and assert it sends**. Verdict, `from_checked_at`, and routability all unchanged. | **Negative control:** with the resolver still failing, an account left `unverified` must **not** send. Proves the test observed "verified survives" rather than "the resolver is not consulted at all." Plus a static assertion: no resolver symbol reachable from `internal/dispatch/`. |
| T7 | Test button re-runs and reports current state | Create against the reject zone (`blocked`), swap the zone to aligned, press Test → `aligned`, `from_checked_at` advanced, account becomes routable | Press Test with the resolver failing → indeterminate reported, verdict **untouched** (§2.5). Distinguishes "re-runs the check" from "recomputes from stored columns." |
| T8 | **The bypass surface** — every write route passes the gate | For each of user create, user PATCH `from_address`, and each bulk row: a `p=reject`-misaligned value produces `blocked`, never an ungated `aligned`/`unverified` row | For each: the **same value via a route that must not launder it** — `POST /enable` on a `blocked` account leaves it `blocked` and non-routable; `PATCH` of `from_name` alone does **not** re-run the gate or alter the verdict. |
| T8b | **The operator surface cannot preserve a stale verdict** (§2.8) | Reach `aligned` via the user route, then `PATCH /v1/admin/accounts/{id}` with a would-BLOCK `from_address`. Assert the row is **not** left `aligned`, and assert it is **not routable** | **The exact bypass, as a test.** Control: the same admin PATCH changing only `from_name` leaves `aligned` intact and the account routable — so the test distinguishes "the gate noticed the address moved" from "any admin PATCH resets the verdict." Second control: `POST /v1/admin/accounts` yields a non-routable row, not a routable ungated one. |
| T8c | **The operator override is explicit, refused by default, and cannot forge `aligned`** (§2.8, M3-P4-OD-9) | Admin PATCH of `from_address` **without** the override flag is **REFUSED** (the write does not land — re-read and assert the old address is still there, not merely that a non-2xx came back). **With** the flag: the write lands, the verdict column is `'unverified'`, the account is **not** routable, and a distinct audit event is recorded carrying the operator identity and the supplied reason | Three controls, because this is a security gate and each clause is separately bypassable. (a) With the flag but **no reason** → refused. (b) With the flag on a From that **would pass** the gate → the verdict is still `'unverified'`, never `aligned` — proving the override cannot mint a verdict even when the address is genuinely fine. (c) **The gate-position control:** the same override flag on a PATCH touching only `from_name` does **not** downgrade a healthy `aligned` row — so the flag's effect is scoped to the address write rather than being a global reset. |
| T13 | **A warn verdict is not usable until acknowledged** (§5.3.1) | Reach `unaligned_warned` (the `p=reject` + SPF-present zone). Before acknowledgment: the account is **not routable** and a job for it stays `queued`. Acknowledge, then re-check: routable, the job dispatches and **actually sends** | **Assert the negative first, and assert it via dispatch, not via a column.** Control: an `aligned` account needs **no** acknowledgment to send — otherwise T13 passes on an account that was unroutable for an unrelated reason (this is T9's failure mode arriving one phase later). Second control: the acknowledgment is refused on an account whose verdict is `blocked` — acknowledgment is not a BLOCK override. |
| T13b | **Acknowledgment is state, not UI — it survives a restart** (§5.3.1) | Acknowledge, restart the Brain, assert the account is still routable and still sends. Read the persisted column directly and assert it is set | Control: a **fresh** account in the same warn state after the same restart is **not** routable — so the test proves the acknowledgment was read from storage rather than that the restart reset the gate to permissive. |
| T13c | **Re-acknowledgment is required when the verdict changes** (§5.3.1) | Acknowledge against `p=none`; then change the zone so the same From is now `p=reject` + no SPF record + no DKIM; re-run the check. The stored acknowledgment must **not** satisfy the new verdict — the account is non-routable (here, `blocked`) | Two controls. (a) A **re-run that produces the same verdict** does *not* invalidate the acknowledgment — the account keeps sending, so the test distinguishes "verdict changed" from "the check ran again." (b) An acknowledgment given against the *new* verdict restores routability, proving the invalidation is re-satisfiable rather than terminal. This is the test that makes M3-P4-OD-10's second column load-bearing: with only a boolean, control (a) and the positive are indistinguishable. |
| T9 | A non-`aligned` account is never selected | `blocked` and `unverified` accounts are not selected by the consume; a job stays `queued` rather than failing | Same account flipped to `aligned` **does** get selected — the same job, the same pool of one, one column different. Without this control T9 passes for an account that was unroutable for an unrelated reason. |
| T10 | Cache behaves, asymmetrically | Two checks inside the TTL issue one lookup set (count queries at the local server); a positive result is reused | An **absence** result is re-queried after `from_dns_negative_cache_ttl_sec` but before `from_dns_cache_ttl_sec` — proving the two TTLs are distinct and the negative one is shorter. A single-TTL implementation fails this. |
| T11 | Config-load ordering invariants | Setting `from_dns_lookup_timeout_ms > from_dns_total_budget_ms` is REJECTED with `config_ordering_violation`; same for the negative/positive TTL pair | The valid boundary case (`==`) is **accepted**. A test that only asserts rejection cannot tell a correct comparison from `<` mistyped, or from rejecting everything. |
| T12 | `from_gate_mode` is honored | `warn_only` downgrades **the T2 zone** (`p=reject` + no SPF record + no DKIM) to `unaligned_warned`; `off` skips DNS entirely (zero queries at the local server) | `enforce` on the same zone blocks. Plus: an unrecognized mode string degrades to `enforce` with a log line, not a panic (§6.2). **Note the zone change:** post-ruling, the only zone that blocks under `enforce` is the three-condition one, so `warn_only` must be tested against *that* zone. Tested against a merely-misaligned `p=reject` zone, T12 would pass trivially — both modes warn — and `warn_only` would be untested. |

T9 is the test that makes `blocked` mean something. Without it, the whole gate is a stored opinion.

T13 is the test that makes M3-P4-OD-1's ruling mean something. The ruling's cost is that the default
path is now permissive, and *"a checkbox the user must tick, not a dismissible toast"* is the entire
thing standing in that path. If the acknowledgment is not enforced at the point of dispatch, the
ruling degrades to "warn and proceed" — which is the outcome it was chosen over, reached by omission.

### 7.3 Where these live

T1–T5, T7, T10–T12 are Brain-side and belong in the `internal/fromgate`/`platformapi`/`adminapi`
packages with the compose DNS service. T8 spans routes and belongs with the admin API tests.

Of the tests added by the ruling: T2b and T3c sit with T2 (same package, same zone fixtures). T3b is a
copy assertion over the response builder and belongs beside it, not in the harness — it needs no DNS
and no DB. T8c belongs with T8/T8b in the admin API tests.

**As actually implemented (Chunks 2-3):** T9, T13, and T13c ended up in `internal/dispatch`'s own Go
test package (`from_gate_eligibility_test.go`, alongside `eligibility_test.go`'s existing suite)
rather than the shell-driven real-infra harness — the dispatch package already has a real
Postgres-backed `TryDispatch`/`recordingPusher` round trip in-package (the same harness Phase 3's own
eligibility tests use), and none of these three properties need a live box or Mailpit to observe: the
job's state transition (`queued`→`sending`) IS the "actually sends" assertion the original §7.2
language called for, proven against real dispatch code, not a stand-in. **T13b** landed in
`platformapi` instead (`TestFromGate_T13b_AcknowledgmentPersistsAndIsReadable`, Chunk 2) reading the
persisted acknowledgment columns directly after a fresh `newTestEnv` clone (a brand-new Server +
Postgres connection, itself standing in for "not the same in-memory state") rather than literally
killing and restarting a live Brain process — sufficient because neither `platformapi` nor
`internal/dispatch` hold ANY in-memory cache of eligibility/acknowledgment state (the only cache in
this phase, `fromGateCache`, caches DNS *lookup* results, never the acknowledgment or verdict
columns), so there is no in-memory state for a literal process restart to distinguish from a fresh
read that a fresh Postgres connection does not already rule out. **T6** similarly landed in
`internal/dispatch` as two tests: a static one asserting `internal/fromgate` is absent from `go list
-deps`'s report of the package's real production dependency graph, and a behavioral one driving
`TryDispatch` for a verified account with a paired unverified-account negative control — both needed
nothing the in-package harness didn't already have.

No test asserts only "no error occurred." Every one asserts a concrete observed post-state — the
verdict column, the routability, the query count, or the response substring.

---

## 8. The routing exclusion

A `blocked` verdict has to prevent sending, or §1's evaluator is decoration. Three candidates:

**(a) Reuse `disabled = TRUE`.** Rejected. `POST /enable` clears it with no DNS re-check
(§2.6) — the gate would be bypassable by a route that never mentions From. This is the bypass the
brief's watch-item is pointing at, and it would be invisible: an operator clearing a block would not
know they had.

**(b) Refuse the write, so no blocked row exists.** Rejected. It contradicts Phase 3 §2.5's
"failure is not a rollback" (the credential is already encrypted and stored by then, and a rollback
discards a row the user can fix with one PATCH), and it leaves the user nothing to press Test on.

**(c) Its own predicate term.** Adopted:

```sql
AND (a.from_verification_state = 'aligned'
     OR (a.from_verification_state = 'unaligned_warned'
         AND a.from_warning_acknowledged_at IS NOT NULL
         AND a.from_warning_acknowledged_for = a.from_verdict_fingerprint))
```

The second acknowledgment term is what makes §5.3.1's "re-acknowledge on a *changed* verdict, not on
a re-run" enforceable **at dispatch** rather than only in the handler that writes it. Note the shape:
the currently-in-force fingerprint has to be readable from the account row for the predicate to
compare against it, so the write path that re-derives a verdict (§2.3's PATCH, the test button)
updates the in-force value and thereby invalidates a stale acknowledgment *by data*, with no
invalidation statement to forget. Whether the in-force fingerprint is a third column or is derived
from columns Phase 1 §4.4 already has is an implementation choice; what is not optional is that the
comparison happens here.

Positive-list, not `<> 'blocked'`, and the difference is load-bearing: the column's default is
`'unverified'`, so a negative test would let **every ungated row send** — including every row
created before this phase and every raw-SQL fixture (§2.7). The positive list makes "never gated"
and "gated and refused" both non-routable, which is the correct posture for a gate.

**Why this is not the flat `IN ('aligned', 'unaligned_warned')` it was on the first pass.**
M3-P4-OD-1's ruling made warn-plus-acknowledgment the *default* verdict for a misconfigured From,
and §5.3.1 makes the acknowledgment persisted state. A flat two-value `IN` list would let an
**unacknowledged** warn row dispatch — which is not a cosmetic gap, it is the ruling reduced to
"warn and proceed," the outcome the ruling was chosen over. The acknowledgment has to be a term in
*this* predicate specifically, because §8 is the only place in the system where "may this account
send" is actually decided; enforcing it in the API layer would leave the same bypass surface §2.8
found for the verdict itself. T13's positive is written against dispatch for exactly this reason.

Two consequences that must not be lost between here and implementation:

- **`aligned` needs no acknowledgment.** Hence the `OR`-shape rather than a single conjoined
  acknowledgment term — a flat `AND <ack>` across both states would make every correctly-configured
  account require a checkbox, which is T13's first control failing.
- **The `accounts_eligibility_idx` `WHERE` clause (§8.1) inherits the whole disjunction**, not just
  the state term, or it stops covering the query.

The columns are settled — **M3-P4-OD-10, ruled 2026-07-30 (§4.6)** — so the predicate above is the
final shape, not a placeholder. The one open implementation choice is how the in-force fingerprint is
supplied (§4.6's option (a)/(b)); it does not change this predicate.

### 8.1 This lands in ONE place, because the predicate is consolidated first — RULED

> **M3-P4-OD-3, RULED 2026-07-31: (a) CONSOLIDATE the predicate to one definition first.** The
> consolidation lands as its **own commit**, before **either** new term — Phase 3's
> `credential_verification_state` (M3-P3-OD-4) or this phase's `from_verification_state`. The reasoning
> recorded: *Gate A/B discipline — a separate commit makes a failure attributable* to the refactor or to
> the term, never ambiguously to both. This section is retained because it documents **what is being
> consolidated and why**, but its conclusion is now settled, not a recommendation.

**The build order, fixed by the ruling:**

| # | Commit | Contains | Green gate |
|---|---|---|---|
| 1 | **the consolidation** | five call sites → one shared definition. **No new term.** No behaviour change | every inherited eligibility/exhaustion/health/report test passes **unchanged** — that is what makes it a pure refactor |
| 2 | Phase 3's term | `credential_verification_state` added to the **one** definition (M3-P3-OD-4) | Phase 3's verification tests |
| 3 | Phase 4's term | `from_verification_state` added to the **one** definition | this phase's tests |

Commit 1 is a refactor of load-bearing SQL with **no** functional change, which is exactly why it is
separable and exactly why it must be separate: if commits 1 and 2 were fused and the dispatcher span,
nobody could say whether the refactor mis-transcribed a clause or the new term was wrong.

**Consolidation acceptance criterion, so "one definition" is provable rather than asserted:** after
commit 1, a grep for the predicate's distinguishing clause returns **one** production site. That is
mechanically checkable and belongs in the same commit as a check, since the whole failure mode being
closed here is the silent re-divergence of copies.

The eligibility predicate is duplicated in five sites — deliberately, per
`dispatch/eligibility.go:288-291`, so exhaustion cannot drift from selection:

- `dispatch/eligibility.go:113-114` — the consume
- `dispatch/eligibility.go:300-301` — `poolHasEligible`, the pre-claim gate
- `dispatch/exhaustion.go:93-94`
- `adminapi/health_store.go:262-263, :361, :393`
- `platformapi/campaign_report.go:178-179`

Plus the partial index `accounts_eligibility_idx` (Phase 1 §3.1), whose `WHERE` clause must gain the
same term or the index stops covering the query.

A term added to four of five sites presents as a **spinning dispatcher**, not a failed send: the
pre-claim gate says work exists, the consume finds nothing, and the loop turns forever. That failure
is silent in exactly the way Rule 10 warns about. Phase 4 is the second consumer to hit this (Phase
1's `deleted_at` was the first), which makes the consolidation a **prerequisite rather than a
nice-to-have**: one shared predicate definition is cheaper than five tests asserting agreement, and
this phase would otherwise write the fifth.

**The partial index is part of the consolidation, not an afterthought.** `accounts_eligibility_idx`'s
`WHERE` clause is a **sixth** copy of the predicate that no Go-side consolidation can absorb — it lives
in SQL DDL. It cannot be deduplicated away, so commit 1 does the next best thing: it puts the index's
clause and the shared definition **adjacent and cross-referenced**, so a term added to one without the
other is visible in review. Both terms (commits 2 and 3) update the index in the same commit that
updates the predicate; an index whose `WHERE` no longer covers the query degrades to a seq scan
silently, which is the performance twin of the spinning-dispatcher failure.

### 8.2 The migration hazard, which is the sharpest thing in this phase

`from_verification_state` defaults to `'unverified'`. The moment `0040` adds the predicate term,
**every existing account stops sending.** In a deployed system that is a silent, total dispatch halt
caused by a metadata-only migration — jobs stay `queued`, nothing errors, and the cause is a
`WHERE` clause.

Three options, and one is a Rule 5 violation:

1. **Backfill by running the real gate** over existing rows before the predicate lands. Correct,
   slow, and needs DNS at migration time.
2. **Backfill `aligned` with `from_checked_at = NOW()`.** **Rejected — this fabricates an outcome.**
   `accounts_from_checked_pairing_check` would accept it, so the database will not save us; it is
   nonetheless a claim that a check happened when none did, which is the same class as inventing a
   send outcome the box never reported (CLAUDE.md Rule 5). Named here so it is not reached for as the
   expedient option under deployment pressure.
3. **Two-step deploy:** ship the evaluator and the entry points first (verdicts start populating),
   then ship the predicate once no `unverified` rows remain.

Recommendation: (3), with (1) for any residue. Registered as **M3-P4-OD-4**. In module 3's current
state the accounts table is empty on every environment, which makes this cheap now and expensive
later — the argument for settling it in this phase.

---

## 9. Mandatory checks, answered

**No config key seeded-but-unconsumed.** All seven in §6.1 have a named consumer at the seeding
commit; the table names it per key. `config_consumption` requires a grep-verifiable consumer
excluding the loader and validators, so the consumer must be the real call site. Three artifacts move
in the same commit or `config_drift` breaks: the seeded row, `Docs/config-manifest.txt`, and
`Docs/config-reference.md`.

**The gate cannot be bypassed by any route that creates or updates a From identity.** **This is the
one mandatory check this document did NOT satisfy on its first pass, and the fix is a design change,
not a wording change.** Enumerated in §2.2 from the tree: **four** production write paths, not two —
the two user routes (create, PATCH) *and* the two surviving operator routes (`POST /v1/admin/accounts`,
`PATCH /v1/admin/accounts/{id}`), which Phase 3 §1.2 explicitly retains and which write `from_address`
without touching `from_verification_state` (§2.8). The consequence of siting the gate in handlers is
that it is bypassable; the resolution is to site it in the **store layer**, so that the writer of the
column is the thing that cannot skip the gate. That is now the rule (§2.8), with the operator-surface
posture as M3-P4-OD-9.

The rest of the surface holds as claimed, each verified rather than assumed: `/v1/run` structurally
cannot write a From (no From in `runReq`, no From column on `jobs`); dispatch reads and never writes;
there is no DEFAULT, trigger, or backfill on either column; the six lifecycle transitions' SET clauses
touch only `quarantined`/`disabled`/`paused`/`pause_reason`/`throttle_paused_at`. Bulk-add is an entry
point Phase 3 left silent on, closed by §2.4. `enable`/`unpause` cannot write the verdict but could
launder it if the exclusion reused `disabled` — §8 rejects that design for this reason, and
`from_verification_state` is confirmed absent from every inherited SET clause, so the adopted predicate
is not clearable by any existing route. Raw-SQL fixtures bypass handlers and are a test-validity
hazard, not a production one (§2.7). T8 asserts each of the four write routes; T9 asserts the exclusion
has teeth.

**`contract_drift`.** The 22-route `ADMIN_ROUTES` literal (`contract_drift.sh:40-70`) and its
`"22 routes gated"` message are unchanged by this phase — **Phase 4 adds no route.** It changes
response bodies and the behavior of routes Phase 3 already registered. Phase 3's M3-P3-OD-11 owns
extending that gate to the platform surface.

**Grants (ADR-0008).** No new table and no new enum type: `from_verification_state` and
`dmarc_policy` were created by Phase 1's `0025`, with `USAGE` settled there. `0039` inserts config
rows; `0040` alters an index and a predicate. `TestGrantsInvariant` must be green regardless — it is
the gate, not the checklist.

**Rule 9 (account→job lock ordering).** Not engaged. The gate writes `accounts` only and writes no
`jobs` row. Per Rule 9's 2026-07-26 semantics it takes no FK `KEY SHARE` beyond its own row. Stated
explicitly because "it writes an account row" reads like Rule 9 territory, and the distinction is
what keeps the rule meaningful. ADR-0010's enumerated list needs no amendment for this phase.

Still true after M3-P4-OD-1's ruling, and worth re-stating because the ruling adds a writer: the
**acknowledgment write** (§5.3.1) is an `accounts`-only write on a user-initiated request. It touches
no `jobs` row, so it does not enter ADR-0010's cycle. What it *does* need is to not race the verdict
it acknowledges — see the re-verification note in §2.5 and T13c's control (a): an acknowledgment and a
concurrent re-check that changes the verdict must not leave an acknowledgment attached to a verdict
that no longer holds. That is a lost-update question on one row, answered by writing the
acknowledgment and its verdict-witness in a **single statement** conditioned on the verdict still
being the one shown to the user, not by adding a lock. Same shape as Phase 5 §2's self-referential
UPDATE, and it is deliberately not a read-then-write.

**Rule 6 (security properties).** The box gains no DNS access, no new frame, and no knowledge of
alignment. The Brain remains sole decryptor. `from_check_detail` holds third-party DNS content and
is never returned verbatim (§5.1). No credential material touches the gate — it reads `from_address`
and `smtp_host`, never the secret.

---

## 10. Open decisions register

> **Consolidated queue: [`Docs/open-decisions.md`](open-decisions.md).** That file is where these are
> ruled from — it records which of them a later phase's ruling already closed, and which bucket
> (BLOCKS / RULE / ACCEPT) each remaining one sits in. This register stays authoritative for full
> context.

| # | Question | Where | Recommendation |
|---|---|---|---|
| M3-P4-OD-1 ✅ **RULED 2026-07-29** | The locked table says *"alignment fails → block"*. Per RFC 7489 §4.2's OR-disjunction, SPF evidence alone cannot establish a DMARC failure | §1.6 | **RULED: the locked rule was wrong.** *"DMARC passing on SPF-aligned **or** DKIM-aligned means DNS can never prove failure."* (1) The default DOWNGRADES to **warn + explicit acknowledgment** — *"a checkbox the user must tick, not a dismissible toast."* (2) BLOCK survives only for `p=reject` **AND no SPF record at all** **AND** no DKIM at any common selector. (3) The deciding argument is the design line: *"users control their own assets… make the failure visible and let them choose."* (4) Copy must match evidence: *"'we couldn't verify this will authenticate,' never 'this will fail.'"* (5) The `Authentication-Results` upgrade path is future work, not V1. Applied in §1.6, §5.2, §5.3, §5.3.1; creates **M3-P4-OD-10** |
| M3-P4-OD-2 ✅ **RULED-ACCEPTED 2026-07-31** | Providers with unguessable per-identity selectors (SES, SendGrid, Postmark) make "no DKIM evidence" indistinguishable from "cannot look." Should they be exempt from BLOCK entirely? | §1.6 | **Still open, but much reduced by M3-P4-OD-1's ruling** — the motivating case (SES customer with DKIM and no SPF *include*) now warns rather than blocks, since BLOCK requires **no SPF record at all**. Residual case: `p=reject` + no SPF record + DKIM-only via an unguessable selector. Recommendation unchanged and now stronger: exempt them — the ruling's own standard is that BLOCK requires evidence approaching proof, and for these providers there is no evidence either way. **Approved as recommended: exempt them.** The generalised standard from M3-P4-OD-1 governs — block only on near-certain evidence, and copy says *"we couldn't verify"*, never *"this will fail"* — which for unguessable-selector providers means never blocking, because "cannot look" is not evidence of absence |
| **M3-P4-OD-10** ✅ **RULED 2026-07-30** *(created by M3-P4-OD-1's ruling)* | The ruling makes **warn + acknowledgment** the default, and §5.3.1 requires the acknowledgment be persisted (state, not UI) and re-required when the verdict changes. **Phase 1 §4.4's column set predates the ruling and has no acknowledgment column**, so there is nowhere to store it | §1.6, §5.3.1, §4.6 | **RULED: add the columns in Phase 4's own `0040`. Do not amend Phase 1.** Three reasons given: (1) *"Phase 1 locked the schema as understood then. My Phase 4 ruling created a requirement it couldn't have anticipated, and an additive column in the discovering phase's own migration is exactly what additive-only is for."* (2) **Direct precedent:** module 2's Phase 5 added `consecutive_failures` + `throttle_paused_at` in `sender_0016` rather than reopening its Phase 1 — verified in tree (`db/migrations/sender_0016_accounts_scoring_columns.up.sql`), including the additive-only and no-new-GRANT rationale this migration reuses. (3) Phase 1's stale `'blocked'` comment is a **doc fix, not a schema change** — corrected in place, in Phase 1 §4.4. Columns specified in **§4.6**; unblocks `0040` without reopening a closed phase |
| M3-P4-OD-3 ✅ **RULED 2026-07-31** | The five-place eligibility-predicate duplication. Consolidate to one shared definition before adding a sixth term, or add the term in five places and test agreement? | §8.1 | Consolidate first. Phase 4 is the second phase to hit this; the divergence failure is a silently spinning dispatcher. **RULED: (a) CONSOLIDATE first**, and the consolidation lands as its **OWN commit** before either new term — *Gate A/B discipline, so a failure is attributable.* Build order: consolidate → Phase 3's `credential_verification_state` (M3-P3-OD-4) → Phase 4's `from_verification_state`. **M3-P4-OD-4 (merged in) resolves with it:** deploy two-step with a real-gate backfill for residue; **never** fabricate `aligned` + `from_checked_at` (§8.2 option 2 stays rejected under Rule 5). Applied in §8.1, §8.2; Phase 3 §2.5/§6.2; Phase 8 §6.3 |
| M3-P4-OD-4 ✅ **MERGED into M3-P4-OD-3, RULED 2026-07-31** | The predicate change halts dispatch for every `unverified` row. Which migration strategy? | §8.2 | Two-step deploy, real-gate backfill for residue. **Never** fabricate `aligned` + `from_checked_at` (Rule 5). **Merged into M3-P4-OD-3 and resolved with it 2026-07-31:** the two questions share one answer, because the build order the consolidation ruling fixes *is* the deploy strategy — commit 3 adds the term, and the residue is drained by a real gate run rather than by a backfill that asserts an outcome nobody observed. §8.2's option 2 (backfilling `aligned` with `from_checked_at = NOW()`) **stays rejected under Rule 5** — it fabricates an outcome the gate never produced, which is the same prohibition that governs the box's send outcomes |
| M3-P4-OD-5 ✅ **RULED-ACCEPTED 2026-07-31** | `net.Resolver` exposes no TTLs, so the cache cannot honor real DNS TTLs. Accept a decoupled short TTL, or take on `miekg/dns`? | §6.5 | Accept decoupled + short. A DNS library is a real dependency for a cache whose worst case is latency. |
| M3-P4-OD-6 ✅ **RULED-ACCEPTED 2026-07-31** | Canary tests against real public domains (§1.1's facts) make CI depend on the public internet | §7.1 | Keep them, marked and separable, allowed to fail loudly. They are the drift signal the §1.4 table has no other source for. |
| M3-P4-OD-7 ✅ **RULED-ACCEPTED 2026-07-31** | `x/net/publicsuffix` bundles a PSL snapshot updated only by module bump | §1.3 | Accept, with a re-vendor check in the Phase 8 hygiene sweep. |
| M3-P4-OD-8 ✅ **RULED-ACCEPTED 2026-07-31** | Persistent indeterminacy: a user whose DNS is durably broken can never create an account. Is there an operator override? | §3.3 | `from_gate_mode` already answers it operationally. No per-account override — that would be a bypass with a UI. |
| M3-P4-OD-9 ✅ **RULED 2026-07-29 — AGAINST the recommendation** *(register row corrected 2026-07-31; see the note under the table)* | **The surviving operator routes write `from_address` without touching the verdict** (Phase 3 §1.2 retains all twelve `adminapi` routes). Run the gate, reset to `'unverified'`, or refuse the write? | §2.8 | **The gate applies at the STORE LAYER to every writer, operator routes included.** Break-glass is an **explicit audited override with a required reason** — its own audit event, writing `'unverified'` only — **never a silent reset.** The recommendation below was **rejected**: ~~*reset to `'unverified'` in the same UPDATE; fail-closed, no DNS dependency, fabricates nothing, costs one Test press*~~. Ruled verbatim: *"If break-glass genuinely needs to set an unverified From, that's an explicit audited override flag, not a side door. Same pattern as break-glass credential read: deliberate and logged, never accidental."* |

**Register correction, 2026-07-31 (Chunk 3 sweep) — recorded rather than quietly overwritten, because
the failure mode is instructive.** M3-P4-OD-9's row above previously showed the **rejected**
recommendation ("reset to `'unverified'` in the same UPDATE") in the ruling column, and marked it
`RULED-ACCEPTED 2026-07-31`. Both were wrong: the item was ruled on **2026-07-29**, and ruled
**against** that recommendation. §2.8 states this correctly — *"the earlier recommendation (b) …
is **not** what was ruled"* — and so does the ledger. Only the register row was stale.

Why it mattered more than a citation slip: the register is the **summary a builder reads**, and the
two designs differ in exactly the way the ruling cared about. A silent reset is *"a side door that
happens to fail closed"*; the ruled design requires the operator to **say** they are bypassing the
gate. A builder working from the stale row would have implemented the side door and passed every test
written against it. This is the general hazard of recording a ruling in the prose and leaving the
register showing the recommendation — the register's *shape* invites it, since the column is titled
for the recommendation.

---

## 11. Exit criteria

1. A From on a `p=reject` domain with **no SPF record at all and no DKIM at any common selector** is
   **blocked at setup**, with copy naming both absent records, the policy, and the fix (T2) — and each
   of those three conditions alone is **not** enough to block (T2b). *(Rewritten by M3-P4-OD-1's
   ruling; the pre-ruling criterion "misaligned + `p=reject` → blocked" is the behaviour the ruling
   eliminated.)*
2. A misaligned From on a `p=none` domain is **allowed with a warning**, and the **warning** is
   asserted, not merely the allow (T3). The same holds for a `p=reject` domain that **does** publish an
   SPF record without this provider's token — warned, not blocked, with the stronger enforcing-policy
   copy variant (T3c).
3. **No warn or block copy claims a failure the evidence cannot support** — the hedged form is
   present and `"will fail"` / `"will be rejected"` are absent, with the banned-phrase check itself
   shown to fire on a seeded bad string (T3b). This is a ruling requirement, not a style preference.
4. **A warned From cannot send until the user acknowledges it**, proven at dispatch rather than by a
   column; the acknowledgment survives a Brain restart; and it is **re-required when the verdict
   changes** but not when an identical verdict is recomputed (T13, T13b, T13c). Without this, the
   ruling's permissive default has nothing standing in it.
5. An aligned From, **including a correctly configured send-as alias**, is allowed — and the
   via-provider alias case is not silently allowed by a token match (T1, T4).
6. A DNS timeout at setup fails closed and is retryable; SERVFAIL behaves the same; NXDOMAIN does
   not (T5).
7. A transient lookup failure does not disable a previously verified From — proven by a real send
   under an induced resolver failure, with a negative control (T6).
8. The test button re-runs the check and reports the current result, including becoming routable
   after the user fixes DNS (T7).
9. **The gate is not bypassable**: all four write routes — user create, user PATCH, **operator create,
   operator PATCH** — pass through it, because it lives in the store layer and not in a handler; every
   bulk row passes through it; `enable` cannot launder a block; `from_name`-only PATCH does not re-run
   it (T8, T8b). An operator PATCH of `from_address` cannot leave a verdict attached to an address the
   gate never saw. Per M3-P4-OD-9, that PATCH is **refused** without an explicit override flag and a
   reason; with them it writes `'unverified'` — never `aligned`, even for an address that would pass —
   and records its own audit event (T8c).
10. A non-`aligned` account is never selected for a send, with the flipped-column control (T9).
10a. **The predicate is ONE definition before either new term lands** (M3-P4-OD-3, §8.1). The
    consolidation ships as its **own commit**, and that commit is green on every inherited
    eligibility/exhaustion/health/report test **unchanged** — that is what proves it was a pure
    refactor rather than a behaviour change wearing one's clothes. Its acceptance check is mechanical
    and ships with it: a grep for the predicate's distinguishing clause returns **one** production
    site, with `accounts_eligibility_idx`'s `WHERE` adjacent and cross-referenced (§8.1). This
    criterion exists because the ruling's whole purpose is attributability: without it, "consolidated
    first" is a build note nothing checks, and commits 1–3 could fuse without any gate noticing.
11. Seven config dials seeded, documented, and **consumed** at the seeding commit; both ordering
   invariants enforced at load with the `==` boundary accepted (T11); `config_drift`,
   `config_consumption`, `contract_drift`, `migration_lint`, and `TestGrantsInvariant` green.
12. `from_gate_mode` honored in all three positions, with safe-default degradation (T12).
13. No DNS resolution reachable from `internal/dispatch/` (T6's static assertion).
14. Docs updated in the same change: this file, `config-reference.md`, `config-manifest.txt`,
    `config-consumers.txt`, the migration doc for `0039`/`0040`, and the user-facing copy contract
    (§5) — Rule 4. Plus the artifact M3-P4-OD-1's ruling created: `future-work.md`'s FW-M3-1 (the
    `Authentication-Results` upgrade path). Phase 1 §4.4's stale `'blocked'` enum comment is already
    corrected — a doc fix, applied in place 2026-07-30, not carried by `0040`.
15. **`0040` adds `from_warning_acknowledged_at` and `from_warning_acknowledged_for`** (§4.6) and its
    header states the three things `sender_0016` states: additive-only, no new `GRANT` (table-level
    DML already covers new columns, so `TestGrantsInvariant` stays green *by construction* rather than
    by being edited), and the back-fill posture. M3-P4-OD-10 is **ruled** — this criterion is now about
    the migration matching the ruling, not about waiting on it.

---

## 12. What this phase does NOT touch

- **The box.** No new frame, no DNS, no resolver, no SMTP change. Phase 3's `push_verify`/
  `verify_report` pair and `stopAfterAuth` are used as-is.
- **`error_class`**, or any of the seven enums. Additive-only, and this phase adds nothing to them.
- **The credential path.** Encryption, KMS, zeroing, and the scrub are untouched.
- **Pacing, reclaim, variation, retention.** Inherited whole.
- **Periodic re-verification.** Explicitly excluded by the build plan; §4.3 records the cost.
- **Bounce ingestion.** Absent in V1, which is why this gate is the only defence and why §4.3's
  stale-ALLOW has no downstream detector.
