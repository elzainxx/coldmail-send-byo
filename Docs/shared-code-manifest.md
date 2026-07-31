# Shared-code manifest

**Its purpose is bug-checking, not synchronisation.** A bug found in either codebase can be
checked against the other, because this file says which areas are still the same code. That is
the whole claim.

## What this explicitly is not

- **Back-porting will not really happen.** Module 2 is finished; module 3 is under active
  development. Fixes flow one direction and module 2 drifts. Reconcile at the all-modules polish
  pass rather than relying on this manifest being continuously maintained.
- Flow is **module 3 → module 2 only**, and in practice **rarely**. Module 2 is sealed.
- Reconciliation happens at the **all-modules polish pass**, not continuously.
- This is **a checking aid, not a sync promise.** A file listed as "inherited verbatim" means *it
  was verbatim at the fork point* (`module3-fork-point`,
  `21b65e65eba8f2cc16a84408bac6e018511ec46b`) — the manifest is a starting position plus a
  mechanical way to ask what has changed since, never an assertion about today.
- Module 2 has never sent through a real provider. Provider-level bugs exist in **two** codebases
  and will be found once. Accepted deliberately (build plan, *Known and accepted*).

## How a check is actually run

The manifest names the commands, so the discipline is mechanical rather than a memory exercise:

- `git merge-base module3-fork-point HEAD` — confirm shared ancestry still resolves.
- `git diff <fork-point>..HEAD -- <path>` — has module 3 touched this file since the fork? Empty
  diff = still verbatim = a module-2 bug applies here unchanged (identifiers aside — Gate B's
  rename touches nearly every file's *text* without touching its *behavior*; read the diff before
  concluding "changed," since a rename-only diff is not a behavior divergence).
- `git log <fork-point>..HEAD -- <path>` — if not verbatim, what changed and in which phase.
- The reverse, run in module 2 against its own HEAD, catches the case where module 2 moved after
  the fork (it should not, but "should not" is not "cannot").

`git merge-base` requires shared history — this is why the fork was a history-preserving `git
clone`, never a directory copy or a squashed import (`phase-0-fork-and-baseline.md` §1.2).

## Structure

One row per inherited area: **area · paths · inherited state at fork · divergence expected at
phase N**.

| Area | Paths | Divergence expected |
|---|---|---|
| Box service (SMTP session, STARTTLS-before-AUTH, memory-only creds + zeroize, outcome buffering) | `services/box/` | **Phase 7, in `smtp.js`** — see the two corrected forecast rows below |
| Content variation (12 toggles, all-off byte-identical) | `services/box/src/variation*.js`, `tests/harness/phase8_variation.sh` | none — build plan: inherited whole |
| Box↔Brain channel (mTLS, register/heartbeat, send stream, persist-ack) | `internal/boxlink/`, `internal/boxproto/`, `src/boxlink/` | none planned |
| Credential encryption (AES-256-GCM, AAD-bound, key-id label check) | `internal/credential/`, `internal/kms/` | Phase 3 (third-party secrets, break-glass read) |
| Job lifecycle, append-only event log, actor GUC | `db/migrations/sender_0003..0008`, `sender_0022` (see the function-body note below) | Phase 1 (additive); Phase 7 replaces `fn_job_events_on_state_change`'s body |
| Idempotency (`provider_message_id`, `attempt_token`) | `sender_0020`, `internal/dispatch/` | none planned |
| Error taxonomy, requeue-only re-dispatch | `internal/dispatch/classify.go` | Phase 5 (dual quarantine triggers) |
| Pacing (per-lane random delay) | `internal/dispatch/` | Phase 6 (per-user tuning) |
| Reclaim loop, heartbeat dead-box detection, lease expiry, startup rescan | `internal/dispatch/reclaim*.go`, `rescan*.go` | none planned |
| Singleton guard incl. ADR-0011 pause + re-acquire | `internal/singleton/`, `sender_0023` | none planned |
| Dispatcher, routing, eligibility | `internal/dispatch/eligibility.go`, `dispatch.go` | Phases 1, 2, 6 — heavy |
| Admin + platform API surfaces | `internal/adminapi/`, `internal/platformapi/` | Phases 3, 5, 6, 7 |
| Migration runner + preflight | `tools/byosmtp-migrate/`, `db/preflight/` | Gate B rename only, applied |
| Harness + 6 CI gates | `tests/harness/`, `.github/workflows/` | Phases 1–7 |
| Cert / secret / provisioning tooling | `tools/gen-certs.sh`, `gen-secrets.sh`, `provision-box/` | Gate B rename only, applied |
| Local dev loop | `scripts/dev.sh`, `Makefile`, `docker-compose.yml` | Gate B rename + host-port offset, applied |

## Two forecast corrections, carried from the plan rather than left for later discovery

The table above already applies these; recorded here so nobody re-derives them:

1. **The box is not untouched.** M3-P7-OD-2 (AUTH PLAIN in scope) and M3-P7-OD-8 (`scrubSecret`
   throws on a zeroed buffer) are both planned changes to `services/box/src/smtp.js`. The
   consequence this manifest exists to deliver: **a module 2 `smtp.js` bug will no longer apply
   to module 3 unchanged once Phase 7 lands.** The direction is not symmetric — module 3 is
   planned to be strictly *more* fail-closed on the credential path.
2. **A migration-resident function body is not covered by a file-based row.** Phase 7 §8 does a
   `CREATE OR REPLACE` on `fn_job_events_on_state_change`. A function's live definition is the
   **last** `CREATE OR REPLACE` in the chain — `sender_0022`, not the `sender_0008` that first
   created it — so `git diff` on `sender_0008` alone returns empty and still tells you nothing
   about whether the function diverged. For SQL-resident logic, check the **defining** migration,
   not the creating one.

## Provenance carried across

Module 2's `Docs/history/` holds 12 locked phase specs, 13 ADRs, its own decision ledger, and its
own build record. Several are load-bearing for **inherited** code — ADR-0008 (grants trap),
ADR-0010 (lock ordering, cited by CLAUDE.md Rule 9), ADR-0011 (singleton lock loss), ADR-0012
(`job_events` FK carve-out). Code comments in inherited files cite these by number; the citations
still resolve because the ADR files came across with the fork (M3-P0-OD-9). Note module 2's own
`Docs/history/README.md` already warns that paths inside those documents predate an earlier
`history/` move — a second fork adds a second layer of the same drift, which is an argument for
marking rather than rewriting when it is found.

## Seeded findings — the module 2 bug log, carried from Phase 8 §7.4

Five entries, each found by a specific phase's planning work rather than by auditing module 2 for
its own sake. This is a record for the all-modules polish pass, not a work queue — module 2 is
sealed and these are not module 3's bugs to fix in module 2's tree. Module 3's own disposition
(fixed / deliberately not back-ported / still latent) is stated per entry so a future reader does
not have to re-derive it.

1. **`contract_drift.sh` off-by-one.** `tests/harness/checks/contract_drift.sh:69` prints *"22
   routes gated"* while the `ADMIN_ROUTES` array and `server.go:45-73` both mount **21** — a count
   no assertion checks. Found by Phase 2 §1.3 (M3-P2-OD-2, ruled 2026-07-30). Module 3's Phase 2
   (G1) removes the 5 pool routes and corrects the printed literal to **16** in that same commit,
   along with `api-contract.md`. **Present, unfixed in module 2.**
2. **`pacer.normalize`'s shrink comment is aspirational, not descriptive.** It states "drop the
   most-ready lanes, keep the ones still owing cooldown," but the code does a positional
   `slots[len-ceiling:]` front-drop, which can forgive cooldown a lane still owes. Found by Phase 6
   §3.3 (M3-P6-OD-1, ruled 2026-07-30), because Phase 6 puts ceiling *shrink* on a normal
   admin-support path for the first time. Module 3 fixes this **as a bug fix, not a behavior
   change** — the ADR records why — selecting by value rather than slice position, tested by T7(d)
   with the front-drop as its own negative control. **Present, unfixed in module 2.**
3. **The credential scrub is complete only for AUTH LOGIN.** AUTH PLAIN's base64 blob defeats it:
   verified by execution, the password's known bytes are absent from the AUTH PLAIN wire form at
   the offset the LOGIN-shaped search expects, because base64's 3-byte grouping means the
   password's bytes never start at a multiple of 3 once prefixed by `\0username\0`. Found by Phase
   7 §1's GAP 1. **Not a module 2 bug in module 2's own threat model** — module 2 chose its own
   SMTP servers, so it chose LOGIN, and the scrub is provably complete there. It becomes live only
   because module 3's Phase 7 (M3-P7-OD-2, ruled 2026-07-31: PLAIN is in scope) lets a third party
   choose the AUTH mechanism. Module 3 fixes its own `smtp.js`; back-porting to module 2 is
   **deliberately not done** — it would widen module 2's scrub for a mechanism module 2 never
   speaks, maintaining a code path that closes no threat there. Trigger to revisit: module 2
   gaining user-supplied SMTP hosts.
4. **`SMTPError`'s eleven safe `console.*` sites depend on constructor argument order.**
   `super(reason)` makes `e.message` the internal slug, with the provider response on a separate
   `.smtpResponse` field; a transposed constructor call is syntactically valid, passes every
   existing test, and would break all eleven log sites **silently**. Found by Phase 7 §10 T8.
   **Present, untested in module 2** — nothing there would catch a future transposition. Module 3
   pins it with an explicit assertion (construct with a known credential; assert `e.message` equals
   the reason and contains none of it) plus a transposition as its own negative control.
5. **The box-side scrub is correct only by statement ordering nothing enforces.** In `stream.js`'s
   `finally`, the report is assembled before `credential.zero()` runs, and `scrubSecret` returns
   text **unchanged** on an already-zeroed buffer — so a scrub moved to after the zero would return
   raw provider text and still report success. Found by Phase 7 §9.1 (M3-P7-OD-8, ruled
   2026-07-31). **Present, unfixed, and silent in module 2 today** — this is the strongest
   back-port candidate in the log, unlike entry 3: it is not closed by module 2's own threat model,
   it is a live risk that a future edit reordering two statements would trip with nothing to catch
   it. Module 3 fixes it two ways (assert the ordering; `scrubSecret` throws on a zeroed buffer) —
   a **behavior change**, not a bug fix, hence its own divergence row above rather than a "rename
   only" entry.

**Also tracked here, not only in `future-work.md`:** `FW-M3-8`, the two Go module vulnerabilities
with live call paths inherited from module 2's own Phase 10 R3 finding. Module 3 forks the same
`go.mod` for all four Go modules, so it forks the same two exposures — **re-verify rather than
inherit the claim**, since an upstream fix may have landed since module 2's Phase 10 close.
