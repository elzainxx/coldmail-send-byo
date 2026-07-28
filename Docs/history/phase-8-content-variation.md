# Phase 8 — Content Variation & Header Engineering (Sender Module)

> **Status: PLAN ONLY.** This document specifies **where** message variation runs,
> **how** the toggle config reaches the box, the **config scope**, the **byte-identical
> safety property**, each of the **twelve locked toggles** (behavior + config key +
> default + independence), the **idempotency/retry interaction**, the **security
> confirmation**, and the **behavioral test plan**. **No implementation code** —
> toggle semantics, the config-delivery contract, the safety guarantee, and testable
> requirements only. It builds on the **locked, closed** Phase 1 schema
> (`phase-1-data-model.md` + `phase1-drafts/`) and the **closed** Phase 2–7 specs.
> Their schemas and contracts are **locked** — Phase 8 builds on them and does **not**
> modify them.
>
> **This phase is intentionally LATE and fully REVERSIBLE. Nothing depends on it.**
> Content variation is a purely-additive presentation/encoding layer bolted onto the
> already-proven Phase 3 send path. Its defining safety property: **with every toggle
> off, the message the box puts on the wire is byte-identical to the Phase 3 send
> path** (§4). Every toggle is an independently-switchable seeded config dial with a
> documented default (§5); the whole set defaults to a no-op.
>
> **Schema-neutral (pending the confirmation in §9).** Phase 8 adds **additive seeded
> config** (`sender_0020`, §6) — the same "each phase introduces config dials" pattern
> as every prior phase — plus **one additive, wire-only Phase 3 protocol extension**
> (an optional `variation` block on `push_job`, §2) recorded as a **coordinated Phase 3
> note**, not a schema change. It adds **no** table, column, enum, constraint, or index,
> and modifies **no** locked schema object. If Phase 8 genuinely needed a schema change
> it would be raised in §9 (Open Decisions) rather than assumed — the two places it was
> tempting (persisting the exact varied bytes; per-contact preheader merge data) are
> **flagged there and deliberately not built**.

**Goal.** Port the reference product's (`MailflowDocs.md` §30.6 Content Variation B1–B8,
§30.7 Header Engineering C1–C6) content-variation and header-realism features as a set
of **independent, config-gated, default-off** toggles applied on the box during MIME
assembly — **without** touching any content decision the Brain owns, any locked
send-path security property, or the idempotency contract. This is **disciplined
integration of existing code as toggles**, not novel design.

---

## Phase 8 — Closeout Delta

This revision folds in **two forward-boundary confirmations** and verifies Phase 8 is
fully closed. **Disposition + consistency only — no new scope, no schema change.** The
all-off = byte-identical-to-Phase-3 property (§4, §8 T1) and the locked send-path security
properties (§8 S1) are **unchanged and not weakened**. What changed:

- **Ruling 1 — the unique link/image param is permanently callback-free (a forward
  boundary, not a change).** The per-recipient `_ref`/`_v` cache-buster (§5 toggle 4) is a
  pure, inert cache-buster and is correctly **not** tracking. A **forward boundary** is now
  stated in §5 toggle 4 and §9 P8-10: this parameter **MUST remain callback-free /
  redirect-free / no-tracking-domain even if open/click tracking is introduced post-V1.**
  If tracking ships later it **must be a SEPARATE mechanism** — this reversible cosmetic
  toggle must **never** be repurposed as the tracking hook / beacon. Stated so a future
  implementer sees the line before wiring anything to it.
- **Ruling 2 — the token-free preheader pool is confirmed body-agnostic filler, and one
  over-claiming template was replaced.** §5 toggle 8 / §9 P8-7 now state that the preheader
  template pool is **generic, body-agnostic filler that can never CONTRADICT or over-claim
  relative to the actual email body** (a preheader asserting something the body doesn't
  support reads as spammy and hurts deliverability). **Consistency fix (flagged, not
  silent):** the prior pool contained `"Following up on my last message"`, which asserts a
  **prior message that single-send V1 (no sequences) does not guarantee exists** — a
  contradiction/over-claim risk. It is **replaced** with the body-agnostic `"A brief note
  for you"` in §5, §6.1, and the `sender_0020` seed (§6.2). No merge/personalization added —
  full personalization stays the deferred **P8-7** future item.

**Still schema-neutral (confirmed).** The closeout changes only prose (§5 toggle 4, §5
toggle 8, §9 P8-7/P8-10) plus **one default-value string inside an already-additive
`sender_0020` config seed** (the preheader template swap). It adds **no** new table,
column, enum, constraint, or index, no new config **key**, and modifies **no** locked
schema object. `sender_0020` remains the latest migration.

---

## Phase 8 — Post-Chunk-5 amendment (2026-07-26, RULED — P8-OD-1)

During Chunk 5 (config seeding), a real contradiction surfaced between the execution
mandate ("seed the toggle keys AND their intensities/rates — comment rate, ZWJ rate,
CSS-variation magnitude, etc.") and this document's own §6.1/§6.2, which enumerated
**exactly fourteen** keys and stated toggles 3/5/10's rates as **fixed values in prose**,
naming no config key for any of them. Filed as `Docs/open-decisions.md` **P8-OD-1** per
Rule 2 rather than resolved unilaterally; Chunk 5 shipped the fourteen-key set unchanged
pending a ruling.

**Ruling (user, 2026-07-26): WIDEN.** Promote all three fixed rates to independent,
documented `sender.config` dials — an on/off toggle whose strength is hardcoded is only
half a dial (config-not-code, CLAUDE.md Rule 1). This is an **additive amendment**, not a
rewrite: the twelve toggles, their behavior, and the byte-identical-when-off property (§4)
are **all unchanged**. Three new keys are appended to §6.1/§6.2's table, growing
`sender_0020` from fourteen keys to **seventeen**:

| New key | Default | `value_type` | Parameter of | Notes |
|---|---|---|---|---|
| `variation_css_magnitude_px` | `1` | integer | toggle 3 (CSS micro-variation) | Max ±px spread (was a fixed ±1 in code). Must be `>= 1`; the "skip <3px, never <0" floor/ceiling behavior is unchanged. |
| `variation_zero_width_rate` | `0.05` | number | toggle 5 (zero-width insertion) | Fraction (0–1) of word-boundary spaces that get a ZWJ (was a fixed 0.05 in code). Toggle 5 itself stays **default OFF**; this parameter is inert while it is off. |
| `variation_user_agent_rate` | `0.3` | number | toggle 10 (User-Agent injection) | Fraction (0–1) of jobs that get an injected UA (was a fixed 0.3 in code, itself ported from the reference's ~30%-inject/~70%-omit split). Inert while toggle 10 is off. |

Each new key is a **parameter of** its toggle (same relationship `variation_comment_intensity`
already has to toggle 2): it is read and applied only while the parent toggle is on, and its
own value is otherwise inert — so it participates in `isNeutral`'s parameter-exclusion rule
exactly like `variation_comment_intensity`/`variation_preheader_templates` already do (§4.2).
`value_type: "number"` is a new value_type (Phase 8's first non-integer numeric key); the
admin API's write-time validator gates it as a JSON number in `[0, 1]` for the two rate keys
and a positive integer for the px key.

**Schema effect.** Still schema-neutral in the table/column/enum sense — three more additive
`sender.config` rows, appended to the SAME `sender_0020` migration file (per the Chunk 5
flagged deviation above; the version stays `sender_0020`, now carrying **seventeen** Phase 8
keys total). `Docs/phase-9-polish-and-documentation.md` §4.5's consolidated table is amended
to match (§4.5's own heading updates from "14 keys" to "17 keys"), and the module total moves
from 39 to 42 pre-Phase-9.5, restoring the eventual 53-key total (was 50) after Phase 9.5's
`sender_0021`.

---

## Phase 8 — What is and is not locked here

**Carried-forward locks this phase respects (not re-litigated):**

- **The Phase 3 send path is the reference baseline (Phase 3 §8, locked).** The
  guaranteed baseline header set (`From`, `To`, `Subject`, `Reply-To` if present,
  `Message-ID`, `Date`) and the box's Phase-3 MIME assembly are the **byte reference**
  Phase 8 is measured against (§4). Phase 3 §8.2 pre-committed this: *"With all Phase-8
  toggles off, output must be byte-identical to this Phase-3 baseline."*
- **The box owns the SMTP session and MIME assembly; the Brain owns every content
  decision (Phase 1 U1, Phase 3 §4/§8).** The Brain decides recipient, subject, body,
  From, Reply-To, account, `Message-ID`, and `Date`; the box relays. Phase 8 lets the
  box **vary the assembled representation** — it does **not** hand the box any decision
  the Brain makes (§1).
- **Idempotency (Phase 1 `#7`, Phase 3 §5.3, locked).** `provider_message_id` is
  `UNIQUE`, persisted on success, checked before resend; the crash-after-`250`
  duplicate window is an **accepted** residual. Phase 8 does **not** change this and
  **must not** weaken it (§7 confirms it slightly *strengthens* the UNIQUE backstop).
- **Credential secrecy (Phase 1 §9, Phase 3 §3.3/§3.4/§4.2/§4.4, locked).** mTLS
  channel, memory-only + zeroed plaintext, STARTTLS-before-AUTH, never-logged. Variation
  touches **only** the outgoing message body/headers/MIME and is entirely disjoint from
  credential/TLS/AUTH handling (§8).
- **Config not code (Phase 1 `sender.config`).** Every toggle is a seeded row with a
  documented default; no behavior is hardcoded (§6).

**Decisions taken in this phase (see §9 for the full log):**

- **P8-1 — variation runs box-side, during MIME assembly** (§1).
- **P8-2 — config reaches the box per-job-fresh in an additive `push_job.variation`
  block** (§2); coordinated Phase 3 protocol note.
- **P8-3 — variation config scope is GLOBAL for V1** (§3); per-pool is the flagged
  future refinement.
- **P8-4 — the variation layer is the identity function when all toggles are off**, so
  all-off output is byte-identical to Phase 3 (§4) — the headline safety property.
- **P8-5 — variation is deterministically seeded from `job_id`; the Brain also derives
  `Message-ID` deterministically from `job_id`** (§7) — so a duplicate-window resend is
  byte-identical (modulo the send-time `Date`), and the `provider_message_id` UNIQUE
  backstop becomes effective on the no-provider-id fallback path. Coordinated Phase 3
  behavior note, with a documented safe opt-out.
- **P8-6 — User-Agent injection is decoupled from header-style** (§5, toggle 10) — a
  deliberate deviation from the reference (which linked C2 to C1) to satisfy the
  no-hidden-coupling requirement.
- **P8-7 — dynamic preheader uses token-free templates in V1** (§5, toggle 8) — the box
  has no per-contact merge data (the platform renders upstream), so merge-token
  personalization in the preheader is **flagged and deferred**, not built.
- **P8-8 — Thread-Index is inert** (§5, toggle 12): the toggle exists, emits nothing in
  V1 (no sequences ⇒ no thread to index).
- **P8-9 — `send_logs` continues to store the Brain-side baseline content, not the box's
  varied bytes** (§1.3); the varied output is **reproducible** from `job_id` + config, so
  it is not separately persisted. Persisting exact varied bytes would be a schema change
  — flagged in §9, not built.

**No locked schema object is modified.** `sender_0020` seeds config rows; the `push_job`
`variation` block is a wire-message field, not a DB object. Every column referenced
already exists in the locked schema.

---

## 0. Constraints this phase is built on (do not violate)

Restated so this document is standalone. **Facts**, not decisions to revisit here.

- **Phase 3 §8.1 baseline headers + assembly.** Every send already carries exactly
  `From`, `To`, `Subject`, `Reply-To` (if the job has one), `Message-ID`
  (Brain-generated `<uuid@sending-domain>`), `Date` (RFC 5322 at send). *"The box sends
  exactly what it is given plus the transport-mandatory MIME/`Content-Type` framing its
  SMTP library adds; it does not invent or reorder headers in Phase 3."* Phase 8 is the
  phase that **turns on** the box's ability to reorder/add/encode — under toggles.
- **Phase 3 §2.3 `push_job` wire message.** Carries `send.{recipient, from_address,
  from_name, reply_to, subject, body, headers}`, the `smtp.*` block (incl. the decrypted
  `secret`), and `lease_expires_at`. Phase 8 adds an **optional** `variation` block to
  this message (§2) — additive, wire-only.
- **The Brain renders nothing new; the platform renders templates.** `rendered_subject`
  / `rendered_body` are **frozen at job creation** (Phase 1), already merge-resolved by
  the platform. Neither the Brain nor the box holds structured contact merge fields at
  send time — this bounds what the dynamic-preheader toggle can do (§5 toggle 8, P8-7).
- **Idempotency + the accepted duplicate (Phase 1 `#7`, Phase 3 §5.3).** A job is `sent`
  **only** on a `report{accepted}` carrying a non-null `provider_message_id`; a
  crash-after-`250`-before-report can produce a **rare duplicate** on reclaim. Phase 8
  keeps this and addresses only the *appearance* of that duplicate (§7).
- **Locked send-path security (Phase 3 §3.3/§3.4/§4.2/§4.4).** mTLS box↔Brain channel;
  box has no DB/KMS; credential memory-only + zeroed on every exit path; STARTTLS
  required before AUTH on 587/25, implicit TLS on 465; credential never logged. **None of
  these is in Phase 8's reach** (§8).
- **Config not code (Phase 1).** All toggles are seeded `sender.config` rows with `{"v":
  …}` JSONB and a documented `value_type` hint (`boolean`/`string`/`array<string>`);
  application validates. Phase 8 introduces its keys by the additive `sender_0020`
  migration (§6).

---

## 1. WHERE variation runs

### 1.1 Box-side, during MIME assembly (P8-1, DECIDED)

The box (Node.js) is the process that assembles the final MIME message and speaks SMTP
(Phase 1 U1, Phase 3 §4). **All variation runs there**, applied to the message
representation between "box received `push_job`" and "box hands the assembled bytes to
its SMTP library's `DATA`." This is exactly where the reference worker applies it
(`MailflowDocs.md` §30.6 `applyContentVariations()` after the Must pipeline; §30.7
`applyHeaderEngineering()` after `buildEmailHeaders()`), so Phase 8 is **porting existing
box-side code**, not relocating logic.

Two ordered layers, mirroring the reference:

1. **Body/content variation** (§5 toggles 1–8), applied to `send.body` (and, for the
   preheader, injected into it): dynamic preheader → HTML minify → comment injection →
   CSS micro-variation → unique URL params → zero-width insertion. Encoding rotation
   (toggle 6) is applied at the MIME-encoding step.
2. **Header/MIME engineering** (§5 toggles 9–12 + boundary/encoding), applied to the
   header set and MIME structure the box hands its SMTP library: header reordering,
   User-Agent injection, precedence control, MIME-boundary rotation, Thread-Index
   (inert).

### 1.2 It moves NO Brain-owned decision onto the box (confirmed)

This is the load-bearing boundary check the task requires. **The Brain still decides
everything it decided in Phase 3:**

| Decision | Owner | Phase 8 effect |
|---|---|---|
| Recipient, Subject, Body, From, Reply-To | **Brain** (frozen at job creation) | unchanged — variation never alters *which* recipient/subject/body/from is sent |
| Account selection, credential | **Brain** | unchanged — not in the variation layer at all |
| `Message-ID` value, `Date` value | **Brain** (generated per §8.1) | value still Brain-generated; §7 makes `Message-ID` **deterministic from `job_id`** but the Brain remains its author |
| Which toggles are active | **Brain reads `sender.config`**, ships the resolved snapshot in `push_job` (§2) | the box **never reads config**; it applies exactly the booleans it is handed |
| The assembled representation (whitespace, comment noise, CSS px, encoding, boundary, header order, UA, precedence, inert URL params, zero-width, hidden preheader span) | **Box** (Phase 8) | the box **varies presentation/encoding only** — it does not choose content |

The box is handed the **resolved** toggle set (booleans/strings/arrays), not the config
table and not any decision logic. It cannot enable a toggle the Brain didn't send, and it
reads no `sender.config`. Variation is a pure function of `(rendered message, resolved
toggles, seed=job_id)` — presentation in, presentation out.

### 1.3 `send_logs` fidelity (P8-9, DECIDED — no schema change)

Phase 3 §7 writes `send_logs` from the box's `report` on the Brain side; §8.1 noted
"`send_logs.headers` and the bytes the box sends are the same set." **With variation on,
that equality no longer holds** — the box reorders/adds headers and re-encodes the body
*after* the Brain built the baseline. Disposition:

- `send_logs` continues to store the **Brain-side baseline** `subject`/`body`/`headers`
  (the pre-variation content), exactly as in Phase 3. **No new column, no schema change.**
- The exact varied bytes are **not** separately persisted. Because variation is
  **deterministically seeded from `job_id`** (§7), the varied output is **reproducible**
  from `job_id` + the config snapshot at send time — it does not need storing to be
  recoverable for debugging.
- **Flagged (§9 P8-9):** if a future audit/deliverability need requires the *exact* wire
  bytes per send, that is a **schema change** (a new column or a `send_log_variations`
  table) and is **out of Phase 8 scope** — recorded as a future option, not built.

---

## 2. HOW the box gets the toggle config (P8-2, DECIDED)

### 2.1 Per-job-fresh in an additive `push_job.variation` block (recommended, chosen)

Config lives in `sender.config` (DB); **the box has no DB access** (Phase 3 §3.4). The
Brain is the sole config reader. Two candidate delivery paths were considered:

| Option | Mechanism | Verdict |
|---|---|---|
| **(a) Per-job-fresh in `push_job`** *(CHOSEN)* | The Brain reads the resolved variation config and includes it as a `variation` block in each `push_job` (§2.2). | **Recommended.** Simplest; boxes stay **stateless/interchangeable** (Phase 1) with no config cache to invalidate; a config change takes effect **on the very next job** with no redeploy and no box restart (honors the Phase 2 exit criterion *"config changes take effect without a redeploy"*); reuses the one channel that already carries everything the box needs per job. |
| (b) Periodic config snapshot pushed to boxes | The Brain pushes a variation-config snapshot to each box on a timer / on change; the box caches it and applies it to subsequent jobs. | **Rejected for V1.** Adds box-side config state + a cache-invalidation/propagation window (a config change is not honored until the next snapshot reaches the box), contradicting the stateless-box model, for **no** benefit at V1 send volumes — the per-job block is a few hundred bytes on a channel already carrying the full rendered body + credential. |

### 2.2 The additive `variation` block on `push_job`

`push_job` (Phase 3 §2.3) gains **one optional field**. This is an **additive Phase 3
protocol extension — a wire message, not a DB table** — recorded as a coordinated Phase 3
note (like P6-11's box-consumed key and P7-4's box-behavior contract), **not a schema
change.**

```json
{
  "type": "push_job",
  "request_id": "req_...",
  "job_id": "uuid",
  "send": { "...": "unchanged Phase 3 fields" },
  "smtp": { "...": "unchanged Phase 3 fields" },
  "lease_expires_at": "2026-07-21T14:05:00Z",

  "variation": {
    "html_minify":            false,
    "comment_injection":      false,
    "comment_intensity":      "low",
    "css_micro":              false,
    "unique_url_params":      false,
    "zero_width":             false,
    "encoding_rotation":      false,
    "mime_boundary_rotation": false,
    "dynamic_preheader":      false,
    "preheader_templates":    ["...7 defaults..."],
    "header_style":           "none",
    "user_agent_injection":   false,
    "precedence":             "auto",
    "thread_index":           false
  }
}
```

- **The `variation` block is the Brain's *resolved* snapshot** of the §6 config keys at
  the moment it built this job — plain booleans/strings/arrays, no logic. The seed is
  **not** a field: it is `job_id`, already present in the message (§7).
- **`variation` absent, or present with every field at its default (all-off / neutral),
  is exactly Phase 3.** A Phase-3-era Brain that never sends the block, or a Brain with
  every toggle at default, produces the byte-identical Phase 3 output (§4). The box treats
  a missing block as all-default.
- **Two-vocabulary rule (Phase 3 §2.6) unaffected.** The block carries no credential, no
  internal reason strings, nothing tenant-facing; it never appears in a platform `report`.

---

## 3. Config scope (P8-3, DECIDED — GLOBAL for V1)

**Variation toggles are GLOBAL in V1** — one operator-owned set in `sender.config`,
applied to every send. Rationale:

- **Users never configure variation.** The locked build-plan context is explicit:
  *"Users supply only a mail list and a template; everything else is handled
  backend-side."* Variation is operator-only tuning (managed through `/v1/admin/config`,
  Phase 2), not a user-facing or campaign-facing knob. Global matches that ownership.
- **Simplest correct scope.** No per-pool/per-campaign resolution, no merge of scoped
  overrides, no ambiguity about which toggle set a given job uses — the Brain reads the
  global keys and stamps them onto every `push_job`.
- **No real V1 need for finer scope was found.** The reference's per-brand/per-campaign
  presets (`MailflowDocs.md` §30.4) exist because that product exposes variation to end
  users; our module does not.

**Flagged future refinement (§9 P8-3): per-pool.** Pools already carry per-pool config
(routing strategy is per-pool, Phase 4), so per-pool variation is the natural first
refinement (e.g., a cold-outreach pool vs a newsletter pool) — **schema-ready, not
built.** Per-campaign is a further step and is not planned for V1. If, in
implementation, a concrete V1 need for per-pool variation surfaces, **STOP and raise it
as an Open Decision** rather than silently widening scope.

---

## 4. The safety property — byte-identical when all off (P8-4, the critical one)

### 4.1 The property

**With every toggle off/neutral, the bytes the box puts on the SMTP wire are identical
to the Phase 3 send path for the same job.** Variation is **purely additive**: off = the
clean, already-tested Phase 3 output. This is the non-negotiable property of the phase and
the headline test (§8 T1).

### 4.2 How it is guaranteed — the variation layer is the identity function when all off

The variation layer is structured as an **ordered sequence of independent transforms,
each strictly gated on its own toggle**:

```
assembled = phase3_assemble(send)          # the exact Phase 3 representation
for t in [preheader, minify, comments, css, url_params, zero_width,
          encoding, boundary, header_style, user_agent, precedence, thread_index]:
    if resolved_toggle(t) is on/non-neutral:
        assembled = apply(t, assembled, seed=job_id)
box_send(assembled)                         # SMTP DATA
```

- With **all** toggles off/neutral, the loop applies **zero** transforms ⇒ `assembled`
  is byte-for-byte `phase3_assemble(send)`. The variation layer is the **identity
  function**. There is no "default reordering," no "default re-encode," no baseline
  comment noise — every effect is behind a toggle that is off.
- The neutral defaults are chosen so "default" *means* "Phase 3": `header_style="none"` =
  **preserve Phase 3 header order** (no reorder); `precedence="auto"` = **omit** the
  Precedence header (Phase 3 emits none); `encoding_rotation=false` = the box's SMTP
  library default encoding (Phase 3's); `mime_boundary_rotation=false` = the library
  default boundary (Phase 3's); every content toggle false = no injected bytes.
- Because Phase 3 itself is non-deterministic in three legitimately-random fields
  (`Message-ID`, `Date`, and the SMTP library's MIME boundary), the **byte-equality test
  pins those three inputs identically for both assemblers** and then asserts equality —
  isolating "does the variation layer alter anything when off" (it must not) from "Phase 3
  is inherently random" (unrelated). See §8 T1.

### 4.3 This is the regression guard for the whole phase

Any future change to a variation transform is safe **iff** T1 still passes: all-off must
remain the Phase 3 identity. The property is what makes the phase "fully reversible —
nothing depends on it": flipping every key back to default restores the proven Phase 3
path exactly.

---

## 5. The twelve toggles — behavior, key, default, independence

All ported from `MailflowDocs.md` §30.6/§30.7. **Each is independently switchable with
no hidden coupling** — toggling one changes only its own effect (§8 T3). All content
toggles are **deterministically seeded from `job_id`** (§7): the same job always yields
the same variation. Config keys and defaults are seeded by `sender_0020` (§6).

| # | Toggle | Config key | Default | Independent? |
|---|---|---|---|---|
| 1 | HTML minification | `variation_html_minify` | `false` | yes |
| 2 | Invisible comment injection | `variation_comment_injection` (+ `variation_comment_intensity`) | `false` (`"low"`) | yes |
| 3 | CSS micro-variation | `variation_css_micro` | `false` | yes |
| 4 | Unique link/image URL params | `variation_unique_url_params` | `false` | yes |
| 5 | Zero-width insertion | `variation_zero_width` | **`false` (OFF)** | yes |
| 6 | Encoding rotation | `variation_encoding_rotation` | `false` | yes |
| 7 | MIME boundary rotation | `variation_mime_boundary_rotation` | `false` | yes |
| 8 | Dynamic preheader | `variation_dynamic_preheader` (+ `variation_preheader_templates`) | `false` | yes |
| 9 | Header reordering by client style | `variation_header_style` | `"none"` | yes |
| 10 | User-Agent injection | `variation_user_agent_injection` | `false` | yes |
| 11 | Precedence header control | `variation_precedence` | `"auto"` | yes |
| 12 | Thread-Index | `variation_thread_index` | `false` (inert) | yes |

### Toggle 1 — HTML minification (`variation_html_minify`, ref B1)
Minifies `send.body` HTML at assembly: remove HTML comments (**preserve** Outlook
conditional comments `<!--[if`), collapse inter-tag and repeated whitespace, trim
line-trailing whitespace, collapse empty lines. **Renders identically** (mail clients
ignore inter-tag whitespace); breaks byte-level content fingerprinting; reduces size.
Off ⇒ body untouched.

### Toggle 2 — Invisible comment injection (`variation_comment_injection` + `variation_comment_intensity`, ref B2)
Injects `<!--` + 8 seeded hex chars + `-->` after a seeded fraction of closing tags.
Intensity → rate: `low` 5%, `medium` 15%, `high` 30%. Comments are **never rendered** →
semantic-equivalent; every message becomes HTML-unique. `variation_comment_intensity` is
a **parameter of** this toggle (ignored when the toggle is off). Off ⇒ no comments.

### Toggle 3 — CSS micro-variation (`variation_css_micro` + `variation_css_magnitude_px`, ref B3)
Varies inline `padding`/`margin` px values by a seeded ±`variation_css_magnitude_px`
(default `1`, amended post-Chunk-5 — see the Post-Chunk-5 amendment above; was a fixed ±1);
**skips values < 3px**, never below 0. Renders **visually equivalent** (no layout change
at the default magnitude); breaks CSS-hash fingerprinting.
`variation_css_magnitude_px` is a **parameter of** this toggle (ignored when off). Off ⇒
CSS untouched.

### Toggle 4 — Unique link/image URL parameters (`variation_unique_url_params`, ref B4+B5) — **NOT tracking**
Appends an **inert, non-callback** cache-busting query param to author-supplied URLs:
`?_ref={hash}` (or `&_ref=`) on `href` links, `?_v={hash}` on image `src`, where `hash`
is derived from `job_id` (per-recipient, since one job = one recipient). **Skips** URLs
already carrying the param. Uses the `_ref`/`_v` prefixes to avoid colliding with an
author's own `ref=` param.
- **This adds NO tracking and NO callback.** It creates **no** new endpoint we control,
  **no** redirect, **no** tracking domain, **no** open/click pixel. It only appends a
  query param to the **existing** destination the author already put in the email; an
  unknown query param is ignored by the destination server. Tracking is out of V1 (build
  plan) and this toggle must **never** become tracking — asserted by §8 T7.
- **Forward boundary (permanent — read before wiring anything to this toggle).** The
  `_ref`/`_v` param **MUST remain callback-free / redirect-free / no-tracking-domain even
  if open/click tracking is introduced post-V1.** This is a *reversible cosmetic toggle*;
  it must **never** be silently repurposed as a tracking beacon. If tracking ships later it
  **must be a SEPARATE mechanism** with its own toggle, endpoint, and consent/scope story —
  not an overload of this cache-buster. A future implementer who needs a tracking hook must
  add one, not reach for `_ref`/`_v`. (§9 P8-10.)
- The single toggle covers **both** links and images (the reference split them into B4/B5;
  we unify to match the locked toggle set — the mechanism is identical). Off ⇒ URLs
  untouched.

### Toggle 5 — Zero-width insertion (`variation_zero_width` + `variation_zero_width_rate`, ref B6) — **DEFAULT OFF**
Inserts **U+200D (ZWJ)** before a seeded `variation_zero_width_rate` fraction (default
`0.05` = ~5%, amended post-Chunk-5 — see the Post-Chunk-5 amendment above; was a fixed
0.05) of word-boundary spaces in visible text nodes. **Default OFF** because some filters
flag zero-width abuse. ZWJ (not U+200B ZWSP) is used because ZWJ renders cleanly across
clients. Zero-width ⇒ **invisible** → semantic-equivalent. Inserts **only** at the
configured rate and **nowhere else** (§8 T5). `variation_zero_width_rate` is a **parameter
of** this toggle (ignored when off). This is the *mild* variant only; the
homoglyph/hidden-text techniques the reference marks "never implement" are **not** built
(§8/§9 P8-12). Off ⇒ text untouched.

### Toggle 6 — Encoding rotation (`variation_encoding_rotation`, ref B7)
Rotates the body part's `Content-Transfer-Encoding` between **quoted-printable** and
**base64** (seeded per job). Same content, different MIME-level encoding → **decodes to
identical bytes** → identical render (§8 T6 asserts decode-equivalence). Off ⇒ the box's
SMTP-library default encoding (= Phase 3's).

### Toggle 7 — MIME boundary rotation (`variation_mime_boundary_rotation`, ref C4)
Varies the MIME multipart **boundary delimiter** style/format (seeded). A boundary is only
a structural delimiter → the **parsed MIME structure is identical** → identical render.
Off ⇒ the library default boundary (= Phase 3's).

### Toggle 8 — Dynamic preheader (`variation_dynamic_preheader` + `variation_preheader_templates`, ref B8)
When on, injects a **hidden preheader span** (`display:none`, 1px, overflow-hidden, with
the reference's Gmail padding: U+200C + NBSP×50) right after `<body>` (prepended if no
`<body>`), choosing one template **seeded from `job_id`**. Hidden span → the **visible
body renders identically**; the preheader populates the inbox preview line (its purpose).
- **P8-7 (DECIDED, flagged): no per-contact merge data box-side.** Our architecture
  renders templates **upstream** (the platform freezes `rendered_body`); neither Brain nor
  box holds structured contact fields (`{first_name}`, `{company}`) at send time (§0).
  So V1 preheader templates use **no merge substitution** — any `{token}` is cleaned to
  empty (matching the reference's "clean up empty tokens" step). The shipped default set
  is the **token-free** subset; `variation_preheader_templates` lets an operator supply
  their own (tokens still cleaned to empty in V1).
- **Body-agnostic filler only (confirmed).** The V1 template pool is **generic,
  body-agnostic filler** — every default template is safe to prepend to **any** email
  because it asserts nothing about the body's content, offer, or history. This is
  deliberate: a preheader that **contradicts or over-claims** relative to the actual body
  (e.g. promising something the body doesn't deliver, or implying a relationship/history
  that doesn't exist) reads as spammy and **hurts deliverability** — the exact opposite of
  this toggle's purpose. The shipped defaults contain **no** relational or content-specific
  claim (in particular, **no** "following up" / "as discussed" / "your order" style
  phrasing that presumes a prior message or a specific body — single-send V1 has no
  sequences, so a "following up" preheader would over-claim). An operator who supplies
  custom `variation_preheader_templates` is responsible for keeping them body-agnostic.
- **Full merge-personalized preheader is deferred** (§9 P8-7): it would require delivering
  per-contact merge data to the box (a further `push_job` extension) + platform
  coordination — **out of Phase 8 scope, not built.** `variation_preheader_templates` is a
  **parameter of** this toggle (ignored when off). Off ⇒ no preheader injected.

### Toggle 9 — Header reordering by client style (`variation_header_style`, ref C1)
`∈ {none, gmail, outlook, apple, thunderbird, random}`. Reorders the **already-decided**
header set to a client's typical order (orders per `MailflowDocs.md` §30.7 C1); known
headers first in the style's order, then any remainder. `random` picks a style seeded per
job. **Reorders only — adds/removes/changes no header value.** Default `"none"` =
**preserve Phase 3 order** (the identity, §4).

### Toggle 10 — User-Agent injection (`variation_user_agent_injection` + `variation_user_agent_rate`, ref C2) — **decoupled (P8-6)**
When on, injects a `User-Agent` header chosen **seeded from `job_id`** (e.g.
`Microsoft-MacOutlook/16.80`, `Mozilla Thunderbird 115.0`; some styles legitimately emit
none) for a seeded `variation_user_agent_rate` fraction of jobs (default `0.3` = ~30%,
amended post-Chunk-5 — see the Post-Chunk-5 amendment above; was a fixed 0.3), reproducing
the reference's ~70%-omission behavior **deterministically** per job.
- **Deliberate deviation from the reference (P8-6):** the reference linked C2 to C1's
  selected style. To satisfy **no hidden coupling** (§8 T3), User-Agent injection is
  **fully independent** of `variation_header_style` — its presence and value are decided
  solely by this toggle + the seed, regardless of the header-style toggle. An operator may
  align them by enabling both, but neither reads the other's state. `variation_user_agent_rate`
  is a **parameter of** this toggle (ignored when off). Off ⇒ no `User-Agent`.

### Toggle 11 — Precedence header control (`variation_precedence`, ref C6)
`∈ {auto, marketing, personal}`. `marketing` → add `Precedence: bulk`; `auto` and
`personal` → **omit** the Precedence header. Default `"auto"` = omit (= Phase 3, which
emits no Precedence). Independent of all other toggles.

### Toggle 12 — Thread-Index (`variation_thread_index`, ref C3) — **inert (P8-8)**
The toggle exists and is wired, but in V1 it **emits no header**: Thread-Index is an
Outlook threading artifact that has **no trigger without sequences**, and the module sends
**single sends only** (build plan locked scope — no sequences). Enabling it is a
documented **no-op**, kept so the wiring exists for a future sequences phase. Asserted
inert by §8 T8. Off and on both emit nothing in V1.

---

## 6. Config keys + the `sender_0020` migration

### 6.1 The seeded keys (all additive, all default to a no-op / neutral)

`sender_0020` seeds the **seventeen** keys below (twelve toggles + five toggle-parameters
— **amended from fourteen to seventeen, post-Chunk-5, RULED — see the "Post-Chunk-5
amendment" section above**: the three rate/magnitude parameters marked ⟪amended⟫ were
originally fixed values in the toggle's own code; the user ruled to promote them to
config dials). All booleans default `false`; the string/array/number keys default to the
Phase-3-neutral value. The whole set at default ⇒ the §4 identity ⇒ byte-identical to
Phase 3.

| Key | Default | `value_type` | Notes |
|---|---|---|---|
| `variation_html_minify` | `false` | boolean | Toggle 1 |
| `variation_comment_injection` | `false` | boolean | Toggle 2 |
| `variation_comment_intensity` | `"low"` | string | param of toggle 2 (`low`/`medium`/`high`) |
| `variation_css_micro` | `false` | boolean | Toggle 3 |
| `variation_css_magnitude_px` | `1` | integer | ⟪amended⟫ param of toggle 3 — max ±px spread |
| `variation_unique_url_params` | `false` | boolean | Toggle 4 — inert cache-buster, **not tracking** |
| `variation_zero_width` | `false` | boolean | Toggle 5 — **DEFAULT OFF (kept off deliberately)** |
| `variation_zero_width_rate` | `0.05` | number | ⟪amended⟫ param of toggle 5 — ZWJ fraction (0–1) |
| `variation_encoding_rotation` | `false` | boolean | Toggle 6 |
| `variation_mime_boundary_rotation` | `false` | boolean | Toggle 7 |
| `variation_dynamic_preheader` | `false` | boolean | Toggle 8 |
| `variation_preheader_templates` | `[7 token-free defaults]` | array<string> | param of toggle 8 (P8-7) |
| `variation_header_style` | `"none"` | string | Toggle 9 (`none`/`gmail`/`outlook`/`apple`/`thunderbird`/`random`); **`none` = preserve Phase 3 order** |
| `variation_user_agent_injection` | `false` | boolean | Toggle 10 — decoupled (P8-6) |
| `variation_user_agent_rate` | `0.3` | number | ⟪amended⟫ param of toggle 10 — injection fraction (0–1) |
| `variation_precedence` | `"auto"` | string | Toggle 11 (`auto`/`marketing`/`personal`); **`auto` = omit** |
| `variation_thread_index` | `false` | boolean | Toggle 12 — inert (P8-8) |

Default `variation_preheader_templates` (token-free per P8-7; body-agnostic filler — no
relational/content-specific claim, per the Closeout Delta ruling 2):
`["A brief note for you", "A quick read for you", "Quick update for you",
"Worth a look", "A quick note for you", "Thought you might find this useful", "Something
relevant for you"]`.

### 6.2 `sender_0020` — seed Phase 8 config keys (additive-only)

Ordered after Phase 7's `sender_0019`; reversible DOWN; same `sender.schema_migrations`
history discipline as every prior migration. **Adds no table/column/enum/constraint/index
— seeds config rows only.**

```sql
-- sender_0020_content_variation_config.up.sql
INSERT INTO sender.config (key, value, default_value, description, value_type) VALUES
('variation_html_minify', '{"v":false}', '{"v":false}',
 'Phase 8 toggle 1: minify HTML body at box-side MIME assembly (strip comments/whitespace; preserve Outlook conditionals). Off = Phase 3 body. Box-consumed via push_job.variation.', 'boolean'),
('variation_comment_injection', '{"v":false}', '{"v":false}',
 'Phase 8 toggle 2: inject invisible HTML comments after a fraction of closing tags. Off = no comments. Box-consumed.', 'boolean'),
('variation_comment_intensity', '{"v":"low"}', '{"v":"low"}',
 'Parameter of variation_comment_injection: low=5%/medium=15%/high=30% of closing tags. Ignored when the toggle is off.', 'string'),
('variation_css_micro', '{"v":false}', '{"v":false}',
 'Phase 8 toggle 3: vary inline padding/margin px by +/- variation_css_magnitude_px (skip <3px, never <0). Off = CSS untouched. Box-consumed.', 'boolean'),
('variation_css_magnitude_px', '{"v":1}', '{"v":1}',
 'Parameter of variation_css_micro (amended post-Chunk-5, P8-OD-1, RULED): max +/-px spread for the seeded CSS variation. Was a fixed +/-1 in code; promoted to a dial. Ignored when the toggle is off.', 'integer'),
('variation_unique_url_params', '{"v":false}', '{"v":false}',
 'Phase 8 toggle 4: append inert cache-busting _ref=/_v= param (seeded from job_id) to author links/images. NOT tracking: no callback, no redirect, no tracking domain. Off = URLs untouched. Box-consumed.', 'boolean'),
('variation_zero_width', '{"v":false}', '{"v":false}',
 'Phase 8 toggle 5: insert U+200D (ZWJ) at a variation_zero_width_rate fraction of word boundaries in visible text. DEFAULT OFF (some filters flag zero-width abuse). Box-consumed.', 'boolean'),
('variation_zero_width_rate', '{"v":0.05}', '{"v":0.05}',
 'Parameter of variation_zero_width (amended post-Chunk-5, P8-OD-1, RULED): fraction (0-1) of word-boundary spaces that get a ZWJ. Was a fixed 0.05 in code; promoted to a dial. Ignored when the toggle is off.', 'number'),
('variation_encoding_rotation', '{"v":false}', '{"v":false}',
 'Phase 8 toggle 6: rotate body Content-Transfer-Encoding between quoted-printable and base64 (seeded). Decodes identically. Off = SMTP-library default (= Phase 3). Box-consumed.', 'boolean'),
('variation_mime_boundary_rotation', '{"v":false}', '{"v":false}',
 'Phase 8 toggle 7: vary the MIME multipart boundary delimiter style (seeded). Structure identical. Off = library default (= Phase 3). Box-consumed.', 'boolean'),
('variation_dynamic_preheader', '{"v":false}', '{"v":false}',
 'Phase 8 toggle 8: inject a hidden preheader span (seeded template) after <body>. V1 = no merge personalization (no contact data box-side; tokens cleaned to empty). Off = none. Box-consumed.', 'boolean'),
('variation_preheader_templates',
 '{"v":["A brief note for you","A quick read for you","Quick update for you","Worth a look","A quick note for you","Thought you might find this useful","Something relevant for you"]}',
 '{"v":["A brief note for you","A quick read for you","Quick update for you","Worth a look","A quick note for you","Thought you might find this useful","Something relevant for you"]}',
 'Parameter of variation_dynamic_preheader: candidate templates (V1 token-free per P8-7). Ignored when the toggle is off.', 'array<string>'),
('variation_header_style', '{"v":"none"}', '{"v":"none"}',
 'Phase 8 toggle 9: reorder headers to a client style (none/gmail/outlook/apple/thunderbird/random). Reorders only; changes no value. none = preserve Phase 3 order (the byte-identical identity). Box-consumed.', 'string'),
('variation_user_agent_injection', '{"v":false}', '{"v":false}',
 'Phase 8 toggle 10: inject a seeded User-Agent header for a variation_user_agent_rate fraction of jobs. Decoupled from variation_header_style (P8-6). Off = no User-Agent. Box-consumed.', 'boolean'),
('variation_user_agent_rate', '{"v":0.3}', '{"v":0.3}',
 'Parameter of variation_user_agent_injection (amended post-Chunk-5, P8-OD-1, RULED): fraction (0-1) of jobs that get an injected User-Agent. Was a fixed 0.3 in code; promoted to a dial. Ignored when the toggle is off.', 'number'),
('variation_precedence', '{"v":"auto"}', '{"v":"auto"}',
 'Phase 8 toggle 11: Precedence header control (auto/marketing/personal). marketing = add Precedence: bulk; auto/personal = omit. auto = Phase 3 (omit). Box-consumed.', 'string'),
('variation_thread_index', '{"v":false}', '{"v":false}',
 'Phase 8 toggle 12: Thread-Index header. INERT in V1 (no sequences => no thread to index); emits nothing whether on or off. Kept for a future sequences phase. Box-consumed.', 'boolean');

INSERT INTO sender.schema_migrations (version) VALUES ('0020');
```

```sql
-- sender_0020_content_variation_config.down.sql
DELETE FROM sender.config WHERE key IN (
  'variation_html_minify','variation_comment_injection','variation_comment_intensity',
  'variation_css_micro','variation_css_magnitude_px','variation_unique_url_params',
  'variation_zero_width','variation_zero_width_rate',
  'variation_encoding_rotation','variation_mime_boundary_rotation',
  'variation_dynamic_preheader','variation_preheader_templates','variation_header_style',
  'variation_user_agent_injection','variation_user_agent_rate','variation_precedence',
  'variation_thread_index'
);
DELETE FROM sender.schema_migrations WHERE version = '0020';
```

The latest migration after Phase 8 is **`sender_0020`**.

---

## 7. Interaction with idempotency & retry (P8-5, DECIDED — deterministic `job_id` seeding)

### 7.1 The problem
Variation includes randomness (comment placement, CSS px, encoding/boundary/style choice,
preheader pick, zero-width positions, the URL hash). A **requeued/reclaimed** job that is
re-pushed would be **re-varied**. And a **duplicate** from the accepted crash-after-`250`
window (Phase 1 `#7`) would produce a **second, different-looking** email to the same
recipient.

### 7.2 Correctness is already safe; only *appearance* is at issue
Idempotency (Phase 3 §5.3) means only jobs that **did not** send are retried — a job with
a `provider_message_id` is never sent again. So the recipient gets **at most one** message
per successful send, and **cross-retry byte-stability is not required for correctness**.
The only gap is the *accepted* duplicate: when it fires, two **different-looking** emails
is a worse artifact than two **identical** ones.

### 7.3 Adopted: deterministic `job_id`-seeded variation **and** deterministic `Message-ID`
- **All box-side variation is seeded from `job_id`** (already in `push_job`; no new
  field). The same `job_id` ⇒ the same variation bytes on every (re)send. A
  duplicate-window resend of the same job therefore produces **byte-identical** body/MIME
  variation, not a different-looking email.
- **The Brain derives `Message-ID` deterministically from `job_id`** (a compatible
  refinement of Phase 3 §8.1's `<uuid@sending-domain>` — the Brain remains the author;
  only the uuid source changes to a stable `job_id`-derived value). Benefits:
  1. **Duplicate copies share one `Message-ID`**, so a resend looks like the *same*
     message — and mail clients that de-dupe by `Message-ID` (e.g. Gmail) collapse the two,
     turning the accepted duplicate into a near-invisible artifact.
  2. **The `provider_message_id` UNIQUE backstop becomes effective on the no-provider-id
     fallback path** (Phase 3 §4.3): with a *random* `Message-ID`, two sends of one job
     produced two different fallback ids, both persistable if the pre-send check ever
     raced; with a *deterministic* `Message-ID`, the second write hits the same value and
     the UNIQUE constraint rejects it → cleanly treated as "already sent." This
     **strengthens** idempotency; it never weakens it (each distinct `job_id` still yields
     a distinct, unique `Message-ID`).
- **Honest limitation — `Date` remains send-time (Phase 3 §8.1).** A reclaim resend
  happens later in wall-clock, so its `Date` header legitimately differs; two copies are
  therefore byte-identical **except** for `Date`. This is correct behavior (Date must
  reflect send time), and it does **not** defeat the mitigation: recipient-facing
  de-duplication keys on `Message-ID`, which **is** identical. We do **not** freeze `Date`
  (that would be a dishonest header).

### 7.4 Safe opt-out (flag, don't force a change to a closed phase)
The `Message-ID` determinism is a Brain-side refinement of the closed Phase 3. It is
**recommended and adopted**, coordinated as a Phase 3 note (§ below). If the user prefers
**not** to touch Phase 3's `Message-ID` generation, the safe fallback is
**variation-only seeding**: variation bytes stay identical across a resend, but
`Message-ID`/`Date` differ, so the accepted duplicate is merely **un-improved** (exactly
Phase 1 `#7` today), never **worsened**. Either way, no invariant changes and no
correctness property is at risk.

### 7.5 Coordinated Phase 3 note
Both facts above are recorded in `phase-3-minimal-send-path.md` as a **Phase 8 coordinated
note** (matching the P6-11 / P7-4 pattern): (1) `push_job` gains the optional additive
`variation` block (wire-only; absent/all-default ⇒ Phase 3 output); (2) the Brain derives
`Message-ID` deterministically from `job_id` (compatible; strengthens the UNIQUE backstop).
**No Phase 3 schema change.**

---

## 8. Behavioral test plan (real behavior)

Real assembly against a **real box process** and a **real SMTP sink** (Phase 3's harness),
parsing **actual bytes**. Each test: Setup → Action → Assertion.

### T1 — ALL TOGGLES OFF ⇒ byte-identical to Phase 3 (the headline regression guard)
- **Setup.** One job. Pin the three legitimately-random inputs (`Message-ID`, `Date`, MIME
  boundary) to fixed known values for **both** assemblers.
- **Action.** Assemble the message via (a) the Phase 3 path and (b) the Phase 8 path with
  a `variation` block where every field is at its §6 default (and, separately, with the
  `variation` block **absent**).
- **Assert.** (a) and (b) are **byte-for-byte identical** — same headers, same order, same
  encoding, same boundary, same body, no injected bytes. The absent-block and all-default
  cases both equal Phase 3. *(This is the identity-function proof of §4; it must pass for
  the phase to be considered correct.)*

### T2 — Each toggle independently switchable; toggling one changes only its own effect
- **Action.** From the all-off baseline, flip exactly **one** toggle on (repeat for all
  twelve). Diff the assembled bytes against the all-off baseline.
- **Assert.** Each single-toggle diff shows **only** that toggle's effect and nothing else
  (e.g. enabling `variation_css_micro` changes only px values; header order, encoding,
  boundary, comments, body text all unchanged). No toggle silently activates or suppresses
  another. **`variation_user_agent_injection` is exercised with `variation_header_style`
  both `none` and a named style** to prove the decoupling (P8-6): UA presence/value is
  identical regardless of header-style.

### T3 — Variation ON ⇒ two sends of the SAME content, DIFFERENT bytes, RENDER identically
- **Setup.** Two **different** jobs (different `job_id`) with the **same** rendered
  subject/body; a representative variation profile on (minify + comments + CSS + url
  params + encoding + boundary + header style).
- **Assert.** (a) The two assembled messages differ at the **byte** level. (b) After
  parsing + decoding transfer-encoding + stripping comments/zero-width + normalizing
  whitespace + resolving links (ignoring the inert `_ref`/`_v` param), the **visible
  rendered body and link destinations are identical** (semantic equivalence). CSS px
  differ by ≤1px with no layout change.

### T4 — Deterministic seeding ⇒ a duplicate-window resend is byte-identical (not a different email)
- **Setup.** One `job_id`, variation profile on.
- **Action.** Assemble the message **twice** for the same `job_id` (simulating the
  reclaim/duplicate-window resend of §7).
- **Assert.** The two assemblies are **byte-identical** in body/MIME variation **and**
  `Message-ID` (deterministic from `job_id`); the only permitted difference is `Date`
  (send-time). *(If the safe opt-out §7.4 is chosen, assert variation bytes identical and
  `Message-ID`/`Date` differ — documented, not a failure.)*

### T5 — Zero-width defaults OFF; enabling inserts at the configured rate and nowhere else
- **Assert.** (a) With `variation_zero_width` at default, the assembled bytes contain
  **zero** U+200D. (b) Enabled, U+200D appears **only** at word-boundary spaces at
  ~the configured rate (seeded, reproducible for the `job_id`) and **nowhere** inside
  tags, attributes, URLs, or headers. Rendering is unchanged (zero-width invisible).

### T6 — Encoding rotation & MIME boundary rotation are decode/parse-equivalent
- **Assert.** With encoding rotation on, the body part's `Content-Transfer-Encoding` is
  quoted-printable **or** base64 (seeded) and **decodes to the identical byte sequence** as
  the off case. With boundary rotation on, the boundary delimiter differs but the **parsed
  MIME tree** (parts, types, order) is identical. Both render identically.

### T7 — Unique URL params are unique per recipient and contain NO tracking/callback
- **Setup.** Two different jobs (recipients), `variation_unique_url_params` on, a body with
  author links + images.
- **Assert.** (a) The `_ref`/`_v` values **differ** between the two recipients and are
  **stable** for a given `job_id`. (b) Each link's/image's **host + path are unchanged** —
  only an inert query param is appended; **no** URL points to a module/tracking domain,
  **no** redirect wrapper, **no** open pixel, **no** callback endpoint is introduced.
  Already-`_ref`-bearing URLs are untouched. Unsubscribe/other reserved URLs (if any in a
  later phase) are skipped.

### T8 — Thread-Index toggle is inert (no header emitted) in the absence of sequences
- **Assert.** With `variation_thread_index` **on** (and off), the assembled message
  contains **no** `Thread-Index` header (single-send V1 has no thread to index). Enabling
  it is a documented no-op.

### T9 — Header style / User-Agent / Precedence behave per config; values never corrupted
- **Assert.** `variation_header_style=gmail/outlook/apple/thunderbird` reorders the
  baseline headers into the documented order **without changing any header value or
  dropping a baseline header**; `random` picks one seeded per job. `variation_precedence`:
  `marketing` adds `Precedence: bulk`, `auto`/`personal` omit it. `variation_user_agent_
  injection` adds the seeded UA (or none) independently of header style.

### S1 — Variation NEVER touches credential/TLS/AUTH or the locked send-path security (security regression)
- **Assert.** With every combination exercised in T1–T9, re-run the Phase 3 security
  suite (§9 S1–S5 there): the credential still never appears in any box log/disk/metric/
  report; STARTTLS-before-AUTH on 587/25 still holds; the credential is still zeroed on
  every exit path; the box↔Brain channel still refuses plaintext/unauthenticated
  transport. The `push_job.variation` block carries **no** credential. **No Phase 3
  security assertion regresses under any variation setting.**

### S2 — The "never-implement" techniques are absent
- **Assert.** No assembled output (any toggle combination) contains Unicode **homoglyph**
  substitution, **hidden text** via `display:none`/`font-size:0` on **visible** content,
  or **fake threading** (`In-Reply-To`/`References` to non-existent messages). Zero-width
  insertion (toggle 5) is the *only* invisible-character technique, and it is default-off.

---

## 9. Open Decisions (decision log)

Every item has an explicit disposition: **DECIDED-NOW**, **FLAGGED-FUTURE** (resolved with
a safe V1 default, deferred work recorded), or **CONFIRMED** (carried-forward lock
restated). **The needs-user list is empty** — every resolve-point the task raised was
answered with the recommended option and a safe, reversible default; each flag below has a
config-off / reproducible / deferred default that requires no user input to ship.

### Decided in this phase
- **P8-1 — variation runs box-side during MIME assembly — DECIDED.** Where the reference
  applies it; moves no Brain-owned decision onto the box (§1).
- **P8-2 — config delivery = per-job-fresh `push_job.variation` block — DECIDED.** Simplest;
  stateless/interchangeable boxes; config-without-redeploy honored on the next job.
  Additive **wire-only** Phase 3 protocol extension, coordinated-noted, **not** a schema
  change. Periodic-snapshot rejected (§2).
- **P8-3 — config scope = GLOBAL for V1 — DECIDED.** Operator-only; users don't configure
  variation. Per-pool is the flagged future refinement (schema-ready via existing pools);
  per-campaign not planned (§3).
- **P8-4 — variation layer = identity when all off ⇒ byte-identical to Phase 3 —
  DECIDED (headline).** Guaranteed by strict per-toggle gating + Phase-3-neutral defaults;
  verified by T1 with the three random inputs pinned (§4, §8 T1).
- **P8-5 — deterministic `job_id`-seeded variation + deterministic `Message-ID` — DECIDED
  (adopt).** Makes a duplicate-window resend byte-identical (modulo send-time `Date`) and
  strengthens the `provider_message_id` UNIQUE backstop on the no-provider-id path.
  Coordinated Phase 3 behavior note; **safe opt-out documented** (§7.4) if the user
  declines the Phase 3 `Message-ID` refinement — fallback = variation-only seeding, which
  never worsens Phase 1 `#7`.
- **P8-6 — User-Agent injection decoupled from header-style — DECIDED.** Deliberate
  deviation from the reference (C2↔C1 link removed) to satisfy no-hidden-coupling; UA is a
  self-contained seeded toggle (§5 toggle 10, §8 T2/T9).
- **P8-8 — Thread-Index inert — DECIDED.** Toggle wired, emits nothing in V1 (no
  sequences); kept for a future sequences phase (§5 toggle 12, §8 T8).
- **P8-10 — unique link/image params = one inert cache-buster toggle, NOT tracking —
  DECIDED, with a permanent forward boundary.** No callback/redirect/tracking domain/pixel;
  per-recipient hash seeded from `job_id`; links+images unified into one toggle to match the
  locked set (§5 toggle 4, §8 T7). **Forward boundary (permanent):** the `_ref`/`_v` param
  **MUST remain callback-free / redirect-free / no-tracking-domain even if open/click
  tracking is introduced post-V1.** This reversible cosmetic toggle must **never** be
  repurposed as a tracking beacon; a future tracking capability must be a **SEPARATE
  mechanism** (own toggle/endpoint/consent), not an overload of this cache-buster. Stated in
  §5 toggle 4 so a future implementer sees the line before wiring anything to it.

### Flagged — resolved with a safe V1 default, deferred work recorded
- **P8-7 — dynamic-preheader merge personalization — FLAGGED-FUTURE; V1 pool confirmed
  body-agnostic filler.** The box has no per-contact merge data (platform renders upstream),
  so V1 preheader templates are token-free (tokens cleaned to empty). Full merge-personalized
  preheaders need a `push_job` merge-data extension + platform coordination — **not built**
  (§5 toggle 8). **Confirmed:** the V1 template pool is **generic, body-agnostic filler that
  can never contradict or over-claim relative to the actual body** (an over-claiming
  preheader reads as spammy and hurts deliverability). **Consistency fix (flagged):** the
  prior default `"Following up on my last message"` presumed a prior message that single-send
  V1 (no sequences) does not guarantee — replaced with the body-agnostic `"A brief note for
  you"` in §5/§6.1/§6.2. No personalization added (that stays this item's deferred future
  work); operator-supplied custom templates must stay body-agnostic.
- **P8-9 — persisting the exact varied wire bytes — FLAGGED-FUTURE.** `send_logs` stores
  the Brain baseline; varied output is reproducible from `job_id` + config, so it is not
  persisted. Storing exact bytes would be a **schema change** (new column / table) — **out
  of scope, not built** (§1.3).

### Confirmed (carried-forward locks, restated — not reopened)
- **P8-11 — CID image embedding (reference C5) is NOT in the locked toggle set —
  CONFIRMED excluded.** The reference marks C5 a stub; it is not among the twelve locked
  Phase 8 toggles and is **not built**.
- **P8-12 — "never-implement" techniques stay excluded — CONFIRMED (security).**
  Homoglyph substitution, `display:none`/`font-size:0` hidden text, and fake threading are
  **not** implemented (reference "Skipped Techniques"; §8 S2). Zero-width (toggle 5) is the
  sole invisible-character technique and defaults off.
- **Idempotency + accepted duplicate (Phase 1 `#7`, Phase 3 §5.3) — CONFIRMED unchanged.**
  Phase 8 only improves the duplicate's *appearance* (§7); the invariant and the accepted
  residual are untouched.
- **Locked send-path security (Phase 3 §3.3/§3.4/§4.2/§4.4) — CONFIRMED untouched.**
  Variation is disjoint from credential/TLS/AUTH handling (§8 S1).

### Needs-user
- **Empty.** No item requires user input to ship. The one item that *touches* a closed
  phase's behavior (P8-5, the `Message-ID` refinement) is adopted with a documented safe
  opt-out (§7.4), so it needs confirmation only if the user wants the opt-out — the default
  is safe either way.

---

## 10. Out of scope for Phase 8 (restated)

- **Phase 9:** `quote` accuracy vs real cost, `describe` tiers/pricing, admin-surface
  completeness, the full config reference (every dial + default + effect), and the API
  contract documentation pass.
- **Anything that changes routing, dispatch, retries, scoring, pacing, or the send-path
  security properties** — Phase 8 changes only the outgoing message's presentation/
  encoding, never any of those.
- **Tracking of any kind** (open/click pixels, callback/redirect URLs, tracking domains) —
  out of V1 (build plan); the unique-URL-params toggle is an **inert** cache-buster and
  must never become tracking (§5 toggle 4, §8 T7).
- **Sequences / threading triggers** — no sequences in V1, so Thread-Index is inert
  (P8-8) and `In-Reply-To`/`References` are not emitted.
- **Per-pool / per-campaign variation scope** — GLOBAL only in V1 (P8-3); per-pool is the
  flagged future refinement.
- **Merge-personalized preheaders** (P8-7) and **persisting exact varied bytes** (P8-9) —
  flagged, deferred, not built (both would need more than Phase 8's additive config).
- **Reference features outside the locked twelve toggles** — the Must-tier features
  (plain-text multipart, Feedback-ID, List-Unsubscribe, etc.), the Sending-Strategy /
  Campaign-Intelligence / Intelligence / Infrastructure Addition tiers, and the Complicated
  tier (`MailflowDocs.md` §30) are **not** in Phase 8's scope; only §30.6 B1–B8 and §30.7
  C1/C2/C4/C6 (+ inert C3) are ported.

---

*This document is standalone and builds on the closed Phase 1–7 specs. It **modifies no
locked schema object** — it seeds additive config (`sender_0020`, §6) and extends the
Phase 3 `push_job` wire message with an optional `variation` block (coordinated Phase 3
note, §2/§7). The three build-plan exit criteria — every toggle independently switchable
with a documented default (§5, §8 T2); all-off output byte-identical to Phase 3 (§4, §8 T1);
variation-on producing different bytes that render identically (§8 T3) — each have a
corresponding behavioral test. §9 is a decision log with an empty needs-user list.*
