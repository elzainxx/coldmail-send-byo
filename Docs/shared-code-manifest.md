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

**Five fields per row, reconciled 2026-08-03 (Phase 8 §7.2)** — the original text promised
*"area · paths · inherited state at fork · divergence expected at phase N · module-2 provenance
reference"* but rendered only three columns for fifteen rows. Reconciled toward **five**, per §7.2's
own recommendation: *"inherited state at fork"* is what makes a row checkable (verbatim vs. already
diverged, independent of trusting the "divergence expected" forecast), and *"module-2 provenance
reference"* is what makes a found bug traceable to the ADR or phase spec that explains the
inherited code, without which a reader re-derives context every time. Both are cheap to state now
and archaeology later. **Fork-state and divergence-observed (not merely "expected") are re-verified
against the tree in this pass, not copied from the original forecast** — per Rule 11, a table
claiming "at fork" is a claim to check, not a comment to trust.

| Area | Paths | Inherited state at fork | Divergence observed (module 3) | Module-2 provenance |
|---|---|---|---|---|
| Worker service (SMTP session, STARTTLS-before-AUTH, memory-only creds + zeroize, outcome buffering) | `services/worker/` | Verbatim (Gate B: identity rename only) | **Phase 7, in `smtp.js`** — see the two corrected forecast rows below; no longer verbatim | `module-2/phase-6-pacing-and-dead-worker-detection.md`, `module-2/phase-7-runtime-hardening.md` |
| Content variation (12 toggles, all-off byte-identical) | `services/worker/src/variation*.js`, `tests/harness/phase8_variation.sh` | Verbatim | none — build plan: inherited whole; still verbatim | `module-2/phase-8-content-variation.md` |
| Worker↔Control channel (mTLS, register/heartbeat, send stream, persist-ack) | `internal/workerlink/`, `internal/workerproto/`, `src/workerlink/` | Verbatim | none planned; still verbatim | `module-2/phase-4-dispatcher-and-routing.md`, ADR-0008 |
| Credential encryption (AES-256-GCM, AAD-bound, key-id label check) | `internal/credential/`, `internal/kms/` | Verbatim | Phase 3 adds the break-glass read path around this code (`internal/adminapi`), the encryption primitives themselves unchanged | `module-2/phase-2-account-management.md` |
| Job lifecycle, append-only event log, actor GUC | `db/migrations/send_byo_0003..0008`, `send_byo_0022` (see the function-body note below) | Verbatim (identifiers renamed `sender`→`send_byo` only, Gate B) | Phase 1 additive (new columns/tables alongside, not touching these); **Phase 7 replaces `fn_job_events_on_state_change`'s body** (defined in `send_byo_0022`, not the `send_byo_0008` that created the function — see the function-body note) | ADR-0008 (grants trap), `module-2/phase-3-minimal-send-path.md` |
| Idempotency (`provider_message_id`, `attempt_token`) | `send_byo_0020`, `internal/dispatch/` | Verbatim | none planned; still verbatim | `module-2/phase-7-runtime-hardening.md` |
| Error taxonomy, requeue-only re-dispatch | `internal/dispatch/classify.go` | Verbatim | Phase 5 (dual quarantine triggers) reads this classifier's output, does not modify it | `module-2/phase-5-error-taxonomy-status-quarantine.md` |
| Pacing (per-lane random delay) | `internal/dispatch/` | Verbatim | Phase 6 (per-user tuning) — see §7.4 entry 2, the shrink-comment bug found exactly here | `module-2/phase-6-pacing-and-dead-worker-detection.md`, ADR-0013 |
| Reclaim loop, heartbeat dead-worker detection, lease expiry, startup rescan | `internal/dispatch/reclaim*.go`, `rescan*.go` | Verbatim | none planned; still verbatim | `module-2/phase-6-pacing-and-dead-worker-detection.md` |
| Singleton guard incl. ADR-0011 pause + re-acquire | `internal/singleton/`, `send_byo_0023` | Verbatim | none planned; still verbatim | ADR-0011 |
| Dispatcher, routing, eligibility | `internal/dispatch/eligibility.go`, `dispatch.go` | Verbatim at fork | Phases 1, 2, 6 — heavy (pool removal, per-user config resolution, routing strategy); no longer verbatim | `module-2/phase-4-dispatcher-and-routing.md`, ADR-0010 |
| Admin + platform API surfaces | `internal/adminapi/`, `internal/platformapi/` | Verbatim at fork | Phases 3, 5, 6, 7 — heavy; no longer verbatim | `module-2/phase-2-account-management.md`, `module-2/phase-9-contract-and-doc-completions.md` |
| Migration runner + preflight | `tools/send-byo-migrate/`, `db/preflight/` | Verbatim except identity rename | Gate B rename only, applied; otherwise still verbatim | `module-2/phase-0-groundwork.md` |
| Harness + 7 CI gates | `tests/harness/`, `.github/workflows/` | Verbatim except identity rename (six gates at fork) | Phases 1–7 add coverage per phase; Phase 7 adds the 7th gate (`user_output_scrub`) | `module-2/phase-9_5-operability-and-control-plane-safety.md` |
| Cert / secret / provisioning tooling | `tools/gen-certs.sh`, `gen-secrets.sh`, `provision-worker/` | Verbatim except identity rename | Gate B rename only, applied; otherwise still verbatim | `module-2/phase-0-groundwork.md` |
| Local dev loop | `scripts/dev.sh`, `Makefile`, `docker-compose.yml` | Verbatim except identity rename | Gate B rename + host-port offset, applied; otherwise still verbatim | `module-2/phase-0-groundwork.md` |

## Two forecast corrections, carried from the plan rather than left for later discovery

The table above already applies these; recorded here so nobody re-derives them:

1. **The worker is not untouched.** M3-P7-OD-2 (AUTH PLAIN in scope) and M3-P7-OD-8 (`scrubSecret`
   throws on a zeroed buffer) are both planned changes to `services/worker/src/smtp.js`. The
   consequence this manifest exists to deliver: **a module 2 `smtp.js` bug will no longer apply
   to module 3 unchanged once Phase 7 lands.** The direction is not symmetric — module 3 is
   planned to be strictly *more* fail-closed on the credential path.
2. **A migration-resident function body is not covered by a file-based row.** Phase 7 §8 does a
   `CREATE OR REPLACE` on `fn_job_events_on_state_change`. A function's live definition is the
   **last** `CREATE OR REPLACE` in the chain — `send_byo_0022`, not the `send_byo_0008` that first
   created it — so `git diff` on `send_byo_0008` alone returns empty and still tells you nothing
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

Eight entries, each found by a specific phase's planning work rather than by auditing module 2 for
its own sake. This is a record for the all-modules polish pass, not a work queue — module 2 is
sealed and these are not module 3's bugs to fix in module 2's tree. Module 3's own disposition
(fixed / deliberately not back-ported / still latent) is stated per entry so a future reader does
not have to re-derive it.

1. **`contract_drift.sh` off-by-one.** `tests/harness/checks/contract_drift.sh:69` prints *"22
   routes gated"* while the `ADMIN_ROUTES` array and `server.go:45-73` both mount **21** — a count
   no assertion checks. Found by Phase 2 §1.3 (M3-P2-OD-2, ruled 2026-07-30). Module 3's Phase 2
   (G1) removes the 5 pool routes and corrects the printed literal to **16** in that same commit,
   along with `api-contract.md`. **Present, unfixed in module 2.**
2. **`README.md`'s "Five blocking CI gates" is stale; CI has run six since `drain_grace` landed.**
   `README.md:88` and its table both list five gates; module 2's own Stage 1 note corrected
   `architecture-overview.md` to six but never touched the README. Found during Phase 0's own Gate
   B execution (M3-P0-OD-10, ruled 2026-07-31) — the second discovery, after entry 1's
   `contract_drift.sh` off-by-one (M3-P2-OD-2, ruled 2026-07-30, the earlier claim, per Phase 8
   §7.4's discovery-date ordering). **Fixed in module 3** as a one-line Gate-B exception to
   "identity changes only" (a factual correction, not a rename) — module 3's `README.md` states
   "Six blocking CI gates" with a `drain_grace` row in the table. **Present, unfixed in module 2.**
3. **`pacer.normalize`'s shrink comment is aspirational, not descriptive.** It states "drop the
   most-ready lanes, keep the ones still owing cooldown," but the code does a positional
   `slots[len-ceiling:]` front-drop, which can forgive cooldown a lane still owes. Found by Phase 6
   §3.3 (M3-P6-OD-1, ruled 2026-07-30), because Phase 6 puts ceiling *shrink* on a normal
   admin-support path for the first time. Module 3 fixes this **as a bug fix, not a behavior
   change** — the ADR records why — selecting by value rather than slice position, tested by T7(d)
   with the front-drop as its own negative control. **Present, unfixed in module 2.**
4. **The credential scrub is complete only for AUTH LOGIN.** AUTH PLAIN's base64 blob defeats it:
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
5. **`SMTPError`'s eleven safe `console.*` sites depend on constructor argument order.**
   `super(reason)` makes `e.message` the internal slug, with the provider response on a separate
   `.smtpResponse` field; a transposed constructor call is syntactically valid, passes every
   existing test, and would break all eleven log sites **silently**. Found by Phase 7 §10 T8.
   **Present, untested in module 2** — nothing there would catch a future transposition. Module 3
   pins it with an explicit assertion (construct with a known credential; assert `e.message` equals
   the reason and contains none of it) plus a transposition as its own negative control.
6. **The worker-side scrub is correct only by statement ordering nothing enforces.** In `stream.js`'s
   `finally`, the report is assembled before `credential.zero()` runs, and `scrubSecret` returns
   text **unchanged** on an already-zeroed buffer — so a scrub moved to after the zero would return
   raw provider text and still report success. Found by Phase 7 §9.1 (M3-P7-OD-8, ruled
   2026-07-31). **Present, unfixed, and silent in module 2 today** — this is the strongest
   back-port candidate in the log, unlike entry 4: it is not closed by module 2's own threat model,
   it is a live risk that a future edit reordering two statements would trip with nothing to catch
   it. Module 3 fixes it two ways (assert the ordering; `scrubSecret` throws on a zeroed buffer) —
   a **behavior change**, not a bug fix, hence its own divergence row above rather than a "rename
   only" entry.
7. **Neither CI workflow builds the migration runner binary its own harness depends on.**
   `tests/harness/run.sh` calls a host-built `tools/*-migrate/bin/*-migrate` binary three times to
   build migrated template databases gating 141 tests across four Go packages
   (`TEST_TEMPLATE_DB`); `.github/workflows/real-infra.yml` runs `gofmt`/`go vet` then
   `scripts/dev.sh certs`/`up`/`test` directly, with **no build step for that binary anywhere** —
   unlike `provision-worker`, which `gen_all_secrets()` builds inline. On a fresh checkout the
   binary is absent, all three `up-and-set-password` calls fail silently (`|| true`), all 141
   gated tests `t.Skip()`, and because `go test` exits 0 on an all-skip package and `run.sh`'s
   `go_suite()` judges pass/fail only by grepping for a `FAIL` line, the harness still prints
   **"58 passed, 0 failed."** Found during module 3's own Phase 0 execution (Gate A's
   evidence-gathering pass), not during planning — the first entry in this log discovered by
   running the fork rather than by reading it. Confirmed identical in module 2's own
   `.github/workflows/real-infra.yml` (same three missing lines). **Present, unfixed in module
   2** — its own "clean-slate reproduction" claim rests on the same silent gap. **Fixed in module
   3** (M3-P0-OD-12, ruled 2026-07-31, fixed the same day): `run.sh` now builds the binary itself
   before its three template-DB steps, and the CI workflow builds it explicitly as its own step
   too. Verified by the standard this manifest itself sets — delete the binary, re-run the
   clean-slate reset and the harness, confirm 58/58 with zero `SKIP` and the binary rebuilt by the
   script, not by a leftover local artifact.

8. **`load_e11.sh`'s throughput-ramp step never restores `concurrency_per_user_default` on exit.**
   The ramp function itself sets this dial once per step (it IS the variable under test), but
   neither this script nor its restore-on-exit trap ever captures or restores its ORIGINAL value —
   unlike the delay/window dials the script already restores. A completed run silently leaves the
   dev stack's `concurrency_per_user_default` at whatever the last ramp step set it to, not the
   seeded default. Found running this exact script during module 3's own Phase 8 §8
   remeasurement — the run finished cleanly, reported real numbers, and STILL left the config
   dial wrong afterward, with nothing in the script's own output to flag it. Confirmed identical
   in module 2's own `tests/harness/load_e11.sh` (same missing capture/restore). **Present,
   unfixed in module 2.** Module 3 fixes it: `ORIG_CONCURRENCY` is captured before the first ramp
   step and restored in `cleanup()` alongside the worker-container teardown.

**Also tracked here, not only in `future-work.md`:** `FW-M3-8`, the two Go module vulnerabilities
with live call paths inherited from module 2's own Phase 10 R3 finding. Module 3 forks the same
`go.mod` for all four Go modules, so it forks the same two exposures — **re-verify rather than
inherit the claim**, since an upstream fix may have landed since module 2's Phase 10 close.

**Re-verified 2026-08-03 (Phase 8 hygiene sweep):** both are STILL LIVE. `services/control/go.mod`
carries `github.com/jackc/pgx/v5 v5.7.4` (fix is v5.9.2+) and `golang.org/x/text v0.21.0` (fix is
v0.39.0+) — `go list -m all` confirms neither has moved since module 2's Phase 10 close. The claim
is not stale.

**`x/net/publicsuffix` re-vendor check (M3-P4-OD-7, ruled 2026-07-31) — run, recorded per Rule 11.**
`go list -m -u golang.org/x/net` from `services/control/` reports a newer release exists:
`v0.21.0 [v0.57.0]`. **Not bumped**, for the identical, already-documented reason `future-work.md`'s
Go-toolchain-upgrade entry gives for `pgx`/`x/text`: `go get golang.org/x/net@v0.57.0` was tried in
isolation and confirmed to force `go 1.23 → go 1.25.0` in `go.mod` (verified directly, then
reverted), which breaks the pinned `golang:1.23-alpine` builder for the same reason the other two
bumps do. This is not a separate defect — it is the SAME builder-pin trap catching a third module,
and the fix is the same one-line-different action already registered: upgrade the builder image
first, then bump all three (`pgx`, `x/text`, `x/net`) together and re-verify the full harness, rather
than bumping `x/net` alone and leaving the builder pinned. **What was safe to fix now, and was
fixed:** `services/control/go.mod` misclassified `golang.org/x/net` as an indirect dependency (`//
indirect`) when `internal/fromgate/dmarc.go` and `evaluator.go` both import
`golang.org/x/net/publicsuffix` directly — moved to the direct `require` block, version unchanged
(`v0.21.0`), zero behavior change, confirmed by `go build ./...` and the full real-infra test suite
staying green.
