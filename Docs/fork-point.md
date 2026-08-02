# Fork point

First commit after the fork, per `Docs/phase-0-fork-and-baseline.md` §1.3. Never rewritten.

## Module 2 repo identity

- Path at fork time: `D:/Mnfi/Codmailling 2`
- Remotes: **none** — the repo existed on one machine only. There was no upstream to fork
  *from* in the hosted sense; the fork is a local history-preserving `git clone` of that path.

## The fork-point commit

Full 40-character SHA: `21b65e65eba8f2cc16a84408bac6e018511ec46b` (`21b65e6`).

`git log -1 --format=fuller 21b65e6`, verbatim:

```
commit 21b65e65eba8f2cc16a84408bac6e018511ec46b
Author:     Phase 0 Build <phase0@sender.local>
AuthorDate: Wed Jul 29 13:17:04 2026 +0300
Commit:     Phase 0 Build <phase0@sender.local>
CommitDate: Wed Jul 29 13:17:04 2026 +0300

    G12-OD-1 ruled (A): job_events' append-only trigger gets a per-FK-column carve-out

    DELETE /v1/admin/accounts/{id} returned 500 for any account that had ever
    been assigned a job, from Phase 1 through Phase 10. sender.job_events
    declares two ON DELETE SET NULL FKs (account_id, box_id); Postgres
    implements SET NULL as an internal UPDATE ONLY ... SET <col> = NULL, and
    sender_0008's unconditional BEFORE UPDATE reject-trigger aborted the FK's
    own maintenance statement, taking the parent DELETE with it.

    [... full body: ADR-0009/ADR-0012 diagnosis, sender_0024's per-column
    carve-out, 5 behavioral tests + 2 migration-lifecycle probes + 3
    anti-vacuity controls, live dev-stack re-drive, doc updates. Preserved
    verbatim in module 2's own history; not re-transcribed here.]

    Verification: 5/5 carve-out tests, full services/brain suite and
    tools/sender-migrate green, all six CI gates PASS, full real-infra harness
    58/58, gofmt/build/vet clean per-module.

    Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
```

Tag applied in module 2: `module3-fork-point` → resolves to `21b65e65eba8f2cc16a84408bac6e018511ec46b`.
Tagging is metadata-only; module 2's tracked tree was not touched (CLAUDE.md Rule 8 governs file
changes, not tags). Verified before and after: `git status --short` in module 2 was empty both times.

## Which module-2 phase state this represents

**Phase 10 close (`d977e16`, "Phase 10: record the four review gates in the build record") plus the
Stages 1–12 pre-deploy walk of `Docs/sender-module-local-test-guide.md`.** Not "Phase 10" alone — the
36 commits between `d977e16` and the fork point are not cleanup, they are a live pre-deploy walk that
found and fixed real defects: the singleton lock-loss dispatch-continuation bug (G11-OD-1/ADR-0011),
this commit's own `job_events` delete-wedge fix (G12-OD-1/ADR-0012), a base64 credential-leak blind
spot in the log-scrub gate, and a missing `stop_grace_period` that let Docker SIGKILL every drain.
Forking at `d977e16` instead would have knowingly imported all four.

## One factual correction, carried from the ruling

The ruling (`Docs/open-decisions.md`, M3-P0-OD-2) says *"(a) HEAD `21b65e6`"*. Verified in module 2's
tree at fork time: `21b65e6` is **HEAD~1**, not HEAD. HEAD is `d34e57a` — a docs-only commit
(`Docs/future-work.md` +1 line, `Docs/install-and-deploy.md` +25 lines, zero non-`Docs/` files). The
explicit SHA governs over the label, per `phase-0-fork-and-baseline.md` §1.1. `21b65e6` stands as the
fork point; module 3's inherited `Docs/future-work.md` does not carry `d34e57a`'s two entries, and
Phase 8's inherited-register reconciliation must add them rather than expect to find them (full
reasoning: `phase-0-fork-and-baseline.md` §1.1, `Docs/open-decisions.md` M3-P0-OD-2).

## Gate A evidence — observed, not predicted

All commands run from `D:/Mnfi/Codmailling 3` against the freshly cloned tree, HEAD reset to
`21b65e6` exactly, `git status --porcelain` empty.

### Tree identity

`diff -rq . "D:/Mnfi/Codmailling 2" --exclude=.git --exclude=.env --exclude=.claude` returns, in full:

- `Docs/future-work.md`, `Docs/install-and-deploy.md` differ — the known 2-file gap between
  `21b65e6` and module 2's current HEAD `d34e57a` (expected; see correction above)
- `Docs/history/mailflow-worker (Sender Inspiration)` (nested foreign git repo), `s11*`/`s12*` scratch
  UUID dumps, `secrets/dev/`, `services/brain/bin/`, `tools/*/bin/`, `*.exe` — all gitignored,
  correctly absent from the clone. This is exactly the class of item §1.2 requires clone-not-copy to
  exclude.

No other file differs. **A raw filesystem diff initially showed every text file as differing** — a
false positive from `core.autocrlf`: module 2's repo has local `core.autocrlf=false`, so its checkout
preserves the stored LF exactly, while the fresh clone had no local override and inherited the
session's global `core.autocrlf=true`, which rewrote every text file to CRLF on checkout. `git show
21b65e6:CLAUDE.md` from both repos confirmed the underlying blobs were already byte-identical. Fixed
by setting `core.autocrlf=false` locally in the module 3 clone and re-running `git reset --hard HEAD`
to force a genuine re-checkout. Recorded because it is exactly the kind of gap that reads as a fork
defect if not run down to the actual cause.

### Stack + harness (first bring-up)

- `bash scripts/dev.sh certs` — minted CA + leaves, dev KMS key, two box tokens, admin key, platform
  token; wrote `.env` (gitignored). No external secret required.
- `bash scripts/dev.sh up` — all four services (`postgres`, `migrate`, `brain`, `box`, `smtp-sink`)
  reached `Healthy`/exit 0 after one `seed` + restart cycle for `box` (registration requires the
  seeded `sender.boxes` row; `up` alone does not seed it — this is documented behavior in
  `scripts/dev.sh`'s own comments, not a fork defect).
- `bash scripts/dev.sh test` — **HARNESS RESULT: 58 passed, 0 failed**, matching module 2's last
  recorded whole-harness figure exactly.
- All **six** gates, run individually: `config_drift` (54 keys, live == manifest == docs),
  `contract_drift` (22 admin routes + 4 platform routes, gated), `migration_lint` (chain contiguous,
  0024 covered), `config_consumption` (54/54 keys have a verified consumer), `drain_grace` (drain 30s
  < stop_grace_period 90s < systemd TimeoutStopSec 120s), `no_secret_leak` (no dev token/hash in any
  service log) — **all exit 0.**
- `gofmt -l services/brain tools/sender-migrate tools/provision-box tests/harness` — empty.
  `go vet ./...` — clean in all four modules.
- Box Node suite (`node --test "test/*.test.mjs"`) — **78 passed, 0 failed, 0 skipped.**
- `docker compose config` (base + `tests/harness/compose.test.yml`) — resolves, no warnings.

### Clean-slate reset

`bash scripts/dev.sh reset` (`down -v` → fresh `postgres` → `migrate` → `seed` → full stack) completed
exit 0. `sender-migrate status` against the reset stack: **25 applied, 0 pending** (`0000`–`0024`).
Both boxes (`dev-box-1`, `dev-box-2`) `active` in `sender.boxes` after reset; box 1's log shows exactly
one `registered over mTLS stream` line — zero eviction churn. The full harness was then **re-run from
that clean slate** (not the warm stack): **58 passed, 0 failed** again, and all six gates re-run green
against the reset stack.

### Skip discipline (the trap this checklist names explicitly)

Confirmed by **reading** the full harness log both times, not by grepping for an absent `SKIP` string
— the module 2 precedent this checklist itself cites is that the only `SKIP` string in a clean log can
be a test *name*. Both harness runs recorded here have **zero** `--- SKIP` lines. That zero is not free;
see the finding immediately below.

## A genuine pre-existing gap this exercise surfaced, flagged per Rule 2

`tests/harness/run.sh` invokes a host-built `tools/sender-migrate/bin/sender-migrate` binary three
times (building three migrated template DBs that gate 141 tests across `tests/harness`,
`internal/adminapi`, `internal/dispatch`, and `internal/platformapi` via `TEST_TEMPLATE_DB`). **No
script anywhere — not `scripts/dev.sh certs`, not `build`, not CI — builds that binary.**
`tools/provision-box` gets an equivalent auto-build inline in `gen_all_secrets()`; `sender-migrate`
does not.

On the fresh clone, before this was noticed, the binary was simply absent. `run.sh`'s three
`up-and-set-password` calls each failed silently (`>/dev/null 2>&1`, `|| true`) and set
`TEST_TEMPLATE_DB=""`; every one of the 141 gated tests then executed `t.Skip("TEST_TEMPLATE_DB not
set")`. Because `go test` exits 0 on an all-skip package, and `go_suite()` (`run.sh:83-96`) judges
pass/fail only by grepping for a `FAIL` line — never by checking for the presence of `SKIP` — every
affected suite still printed `PASS` and the harness still reported **58 passed, 0 failed**. The
`go_suite` echo filter (`grep -E "^(ok|FAIL|--- (PASS|FAIL))"`, `run.sh:94`) additionally drops `SKIP`
lines from what a human reads, so nothing in the printed log hints at it either.

Confirmed by building `tools/sender-migrate/bin/sender-migrate` by hand (`go build`, no file changed)
and re-running: all 141 previously-skipped tests ran and **passed**, with zero regressions. This
establishes the gap is a **missing build step**, not a defect in the tested code — but it is a real
gap in module 2's own inherited harness, exposed here for the first time because Gate A's
byte-identical-clone methodology is the first time this harness has run against a tree that never had
the binary built by some earlier, unrecorded manual step.

**Disposition at the time this section was written:** flagged to the Open Decisions ledger rather
than silently fixed (Rule 2 — this is a module-2 defect surfacing in inherited code, not a module-3
design choice). Phase 0 does not patch `run.sh` at this point in the record; every harness run in
this document past this point builds the binary first, matching what `gen_all_secrets()` already
does for `provision-box`.

**Superseded same day:** `Docs/open-decisions.md`'s M3-P0-OD-12 later ruled the opposite — fix it in
Phase 0, now, because a clean-slate reset that depends on a binary already on disk is not actually
clean-slate, and that is Phase 0's own exit criterion. `run.sh` and the CI workflow were both patched
before Phase 0 closed. This section is left as the historical record of Gate A's own evidence and is
not rewritten; see M3-P0-OD-12 for the fix and its own from-a-deleted-binary verification.

## Gate B evidence — observed, not predicted (added 2026-08-02, landing-check backfill)

**This section was missing from the original Gate B commit** (`f7be832`, "Gate B: rename module
identity sender -> byosmtp, nothing else") — the rename genuinely happened and was genuinely
verified against real infra, but the evidence lived only in that commit's own message, never
transcribed here where §7's exit criteria expect it. Found by Phase 6's now-permanent mechanical
landing check (M3-P0-OD-1): §7's Gate B checkboxes were still all unchecked despite the work being
done. Backfilled from `f7be832`'s commit message, which is the authoritative record — nothing new
was run to produce this section; it restates what that commit already proved.

- `git diff <module3-fork-point tag>..f7be832` — identity literals only (schema/role/table names,
  Go module paths + import statements, npm package name, binary/dir rename, CODEOWNERS handles,
  advisory-lock band, host ports, dev CA CN, systemd unit names, the `"module"` wire-identity
  string). Verified by **reading** the diff, not just the file list — no behavior change present.
- Residual-literal sweep: `sender` schema renamed across ~1,105 non-`Docs/` occurrences in ~169
  files; migration FILENAMES deliberately kept the `sender_NNNN` prefix (M3-P0-OD-4's deferral to
  Phase 1) — only file contents were renamed.
- Full harness (`bash scripts/dev.sh test`) — **58 passed, 0 failed, zero SKIP** — run **twice**:
  once on first bring-up after the rename, once again after a genuine `down -v` clean-slate reset.
  Both runs green.
- All **six** CI gates green, both times (first bring-up and after the clean-slate reset):
  `config_drift`, `contract_drift`, `migration_lint`, `config_consumption`, `drain_grace`,
  `no_secret_leak`.
- Clean-slate reset: migration status **25 applied, 0 pending** against the reset stack; both boxes
  `active` with zero registration eviction churn.
- `gofmt -l` empty across all four Go modules (after `gofmt -w` fixed alignment drift the longer
  `byosmtp` identifier introduced); `go vet ./...` clean in all four. Box Node suite **78/78**.
- `docker compose config` (base + harness override) resolves with no warnings.
- Three real gaps the mechanical rename pass missed on its first attempt, caught only by actually
  bringing the stack up rather than trusting a clean diff: (1) two `CREATE`/`DROP SCHEMA`
  statements whose `sender` token had a `NOT EXISTS`/`IF EXISTS` clause between the keyword and the
  name, breaking migration `0000`'s naive regex and failing bring-up outright; (2)
  `migration_lint.sh` still grepped migration file CONTENT for the pre-rename tracking-table name;
  (3) three SQL/test-helper-argument sites in `tools/byosmtp-migrate`'s own integration tests that
  would have silently queried a schema that no longer existed. All three fixed in the same commit.

**§7's Gate A and Gate B checkboxes are checked off below against this evidence and Gate A's own
section above** — neither list was ever literally ticked in `phase-0-fork-and-baseline.md` despite
every item being satisfied; see that document's own updated §7 for the tick-off.

## Known limits of this evidence

Module 2 has **never sent through a real provider** — this baseline is Mailpit-green only. Every
"green" recorded above means "the fork is intact," never "the code is provider-validated." Sending
through a real provider is out of scope for the whole build until the all-modules test pass
(`phase-0-fork-and-baseline.md` §6).

## One-way divergence statement

Fixes flow **module 3 → module 2 only**, and in practice **rarely** — module 2 is sealed and will
drift from module 3 without reconciliation. Reconciliation happens at the all-modules polish pass, not
continuously. `Docs/shared-code-manifest.md` (§5, this phase) is the checking aid for this; it is not a
sync promise.

## Deferred: git remote for disaster recovery

Phase 0's own instruction ("add a git remote — local git protects against edit mistakes, not disk
failure") is **deliberately not done yet**, by explicit user instruction during Chunk 3
("skip pushing for now"). This repo currently has **no remote**, unlike module 3's planning-docs
repo (`zexSites/codmailling-module-3-docs`, private). Recorded here so it is a known, named gap
rather than a silent omission — `git remote add origin <url> && git push -u origin master` when
ready. Until then, this working tree is the only copy.
