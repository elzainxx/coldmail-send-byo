# Phase 0 — Fork and baseline

> **STATUS: PLAN ONLY.** No code, no migrations, no file moves, no renames performed by this
> document. When executed, this phase produces a new repository containing module 2's shipped
> system, renamed to module 3's identity, with the inherited harness and CI gates green — and
> **nothing else**. No schema change, no feature, no deletion. Those are Phases 1–8.

**Authority.** `Docs/module-3-build-plan.md` is the roadmap; its "Phase 0 — Fork and baseline"
section is what this document makes executable. Module 2's Phase 0 output is **inherited, not
redesigned** — where this document needs to describe the harness, the CI gates, the migration
runner, the docs discipline, or the secrets/cert scaffolding, it **references**
`Codmailling 2/Docs/history/phase-0-groundwork.md` (§1–§9) and does not restate the design.

**The one thing this phase proves:** the inherited system runs green in the new repo before a
single line is changed. If the fork is broken, it is broken on day one, not at Phase 4.

---

## 0. The sequencing problem, and the two-gate structure

The build plan's Phase 0 lists "renamed services" under **Builds** and "the inherited harness runs
green in the new repo **before a single line is changed**" under **Exit criteria**. A rename *is* a
change. Read literally, the two cannot both be satisfied by one gate.

Phase 0 therefore runs **two gates in sequence**, and both must be recorded:

| Gate | Tree state | Proves |
|---|---|---|
| **Gate A — inheritance** | Byte-identical to the fork point. Zero edits. | The fork carried everything: the stack builds, comes up, the harness passes, all six CI gates pass, the migration chain applies from an empty volume. A failure here is a **fork defect** or an **environment defect** — never a module-3 defect. |
| **Gate B — identity** | Rename applied (§2). Nothing else. | The rename is complete and non-breaking. Every failure between A and B is attributable to exactly one mechanical rename, because nothing else moved. |

This ordering is the whole value of the phase. Renaming first and then running the harness once
produces a single ambiguous verdict; a red harness could mean a bad fork or a missed rename, and
the two have completely different remedies. Gate A must be green **and recorded** before any
rename begins. See **M3-P0-OD-1** for the ruling on whether this reading is adopted.

---

## 1. Fork mechanics

### 1.1 Identifying the fork point

Module 2's repo (`D:/Mnfi/Codmailling 2`) as observed:

| Fact | Value | Consequence |
|---|---|---|
| Branches | `master` only | No branch ambiguity. |
| Tags | **none** | The fork point can be named only by SHA today. |
| Remotes | **none** | The repo exists on one machine. There is no upstream to fork *from* in the hosted sense. |
| Commits | 172 | — |
| Working tree | clean (`git status --porcelain` empty) | HEAD is an unambiguous snapshot. |
| HEAD | `21b65e65eba8f2cc16a84408bac6e018511ec46b` (`21b65e6`, 2026-07-29) — *"G12-OD-1 ruled (A): job_events' append-only trigger gets a per-FK-column carve-out"* | — |

**"Post-Phase-10" is not a single commit.** The formal Phase 10 close is `d977e16` *("Phase 10:
record the four review gates in the build record")*. HEAD is **36 commits later**, and those 36
commits are not cleanup — they are the live pre-deploy walk of
`Docs/sender-module-local-test-guide.md` (Stages 1–12), which found and fixed real defects in
inherited code:

| Post-close fix | Commit | Why it matters to a fork |
|---|---|---|
| Singleton lock-loss keeps dispatching → two live Brains (G11-OD-1 / ADR-0011) | `8122b09` + `sender_0023` | HIGH. Rule 7's "exactly one dispatcher" only held from startup before this. |
| `job_events` SET-NULL FK vs append-only trigger → used account cannot be deleted (G12-OD-1 / ADR-0012) | `21b65e6` + `sender_0024` | Module 3 **requires** account deletion + retention purge (Phase 3). Forking before this inherits a wedged delete path into the phase that depends on it. |
| Credential leak gates grepped plaintext only — base64 leak invisible | `9daa622` | Module 3 surfaces full provider responses to users. This gate is load-bearing there. |
| `migration_lint` could not see a migration absent from `wantChain` | `bc9b74e` | The gate that guards the chain module 3 renumbers at Phase 1. |
| `docker-compose.yml` had no `stop_grace_period`; Docker SIGKILLed every drain | `30867e2` | Every graceful-shutdown property was unprovable in the dev stack. |
| Config type fault misreported as DB outage; `quote`/`report`/lifecycle refusals undiagnosable | `a617fda`, `fe27588`, `12dade8`, `6004cd7`, `b290953` | Module 3's support load is explicitly higher (build plan, *Known and accepted*). |

**Identification rule (how to name the fork point, not which one to pick):**

1. The fork point is a **full 40-character SHA**, never a branch name, never "HEAD", never "latest".
2. It must be a commit at which the whole-system evidence is recorded — i.e. the harness, the six
   gates, and the clean-slate reset were observed green at or after it. `Docs/sender-module-local-test-guide.md`
   Part 5 is that record; each ticked line cites the commit that settled it.
3. It must have a clean working tree at capture time (verified above for HEAD).
4. **Tag it in module 2** as `module3-fork-point` before forking. Module 2 has no tags and no
   remote; a bare SHA in a second repo is the only link between them, and it survives nothing.
   A tag is metadata-only and does not alter module 2's sealed history (CLAUDE.md Rule 8 governs
   *file* changes; an annotated tag touches no tracked file).

**Fork-point selection: RULED 2026-07-31 (M3-P0-OD-2) — `21b65e6`**, i.e.
`21b65e65eba8f2cc16a84408bac6e018511ec46b` (resolved in module 2's tree; criterion (1) forbids anything
but the full 40-character form in the recorded artifact, so that is the string the tag and this doc
carry). Tag it in module 2 as `module3-fork-point`, per (4) above.

The four criteria hold at that commit rather than being asserted: clean tree, and at or after every
commit the Part 5 sign-off lines cite.

> **One factual correction to the ruling's wording, recorded rather than silently applied.** The ruling
> says *"(a) HEAD `21b65e6`"*. Verified in module 2's tree on 2026-07-31: `21b65e6` is **HEAD~1**, not
> HEAD. HEAD is `d34e57a` — *"Docs: register the five-site eligibility duplication; name the 0024
> rollback hazard"* — a **docs-only** commit touching `Docs/future-work.md` and
> `Docs/install-and-deploy.md` and no code.
>
> **The explicit SHA is taken as authoritative over the label**, because a SHA is unambiguous and
> "HEAD" is a moving reference — which is criterion (1)'s entire point. So the fork point is
> `21b65e6`, as ruled.
>
> **The label ambiguity is CLOSED 2026-07-31, and the reading is confirmed rather than merely assumed.**
> The two candidate readings turn out to be **materially identical for code**: `git diff 21b65e6 d34e57a`
> touches exactly `Docs/future-work.md` (+1 line) and `Docs/install-and-deploy.md` (+25) and **zero**
> non-`Docs/` files. So no source file, migration, test, or config differs between the two, and the
> choice cannot affect a single inherited behaviour — it affects only which two `future-work.md` entries
> module 3 inherits, which the paragraph below already handles. Recorded so a later reader does not
> reopen this looking for a hidden code delta: **there is none.** `21b65e6` stands, and `d34e57a`'s two
> entries are **added** by Phase 8's reconciliation rather than read as drift.
>
> **The consequence, stated because it is not zero:** `d34e57a` is the commit in which module 2
> registered *the five-site eligibility duplication* and *the `sender_0024` rollback hazard* in its own
> docs. Forking one commit earlier means module 3's inherited `Docs/future-work.md` does **not** carry
> those two entries. Neither fact is lost — module 3's own plans carry both, in more detail (Phase 4
> §8.1 owns the predicate consolidation under M3-P4-OD-3; Phase 3 §6.3 owns the `0024` dependency) —
> so nothing is unrecorded. But **Phase 8's inherited-register reconciliation (§4.1) must add these two
> entries rather than expecting to find them**, and if it diffs module 3's future-work against module
> 2's *current* HEAD it will see two entries missing and must not read that as drift. Noted here
> because it is precisely the kind of one-commit gap that reads as an error six months later.
>
> If the intent was in fact *"whatever module 2's HEAD is at fork time"* rather than the named SHA, this
> is a one-line change (fork `d34e57a`, drop the reconciliation note above) — flagged in
> `Docs/plan-revision-log.md` rather than decided here.

### 1.2 How the fork is taken

**Clone the git repository. Do not copy the directory.** This is a security requirement, not a
preference. Module 2's working directory contains, all gitignored and therefore absent from a
clone but present in a filesystem copy:

- `.env` — live dev credentials (module password, admin key, platform token, box tokens)
- `secrets/dev/certs/{ca,brain,box,sink}.{crt,key}` + `ca.srl` — private keys
- `secrets/dev/kms/dev-master.key` — the dev credential-encryption master key
- `Docs/history/mailflow-worker (Sender Inspiration)/` — a **nested foreign git repo**
- `tools/*/bin/*`, `*.exe` — compiled binaries
- `s11*.ids/.items`, `s12*.ids/.items` — 24 scratch UUID dumps from the Stage 11/12 walk

A copy inherits module 2's dev keys into module 3, which silently couples two modules'
"independent" dev stacks to one key. A clone inherits none of it; module 3 mints its own via the
inherited `scripts/dev.sh certs` (module 2 §6).

**Preserve full history.** Clone with history intact — do not squash-import or copy-then-`git init`.
Shared ancestry is what makes divergence traceable: `git merge-base`, `git diff <fork-point>..HEAD`,
and `git log <fork-point>..HEAD` are the shared-code manifest's actual verification tools (§5).
A squashed import reduces the manifest to prose that cannot be checked.

Module 3's working directory currently holds only `Docs/`, and **as of 2026-07-30 it is a git
repository** — but *not* the forked one. It was `git init`ed (first commit `366a675`, *"Module 3
planning docs, pre-fork snapshot"*) for one reason: by then **six phase specs plus the build plan, two
diagrams, and the deferred register were sitting unversioned on disk**, and a directory mishap would
have cost the entire planning effort. It has no relationship to module 2's history.

**It now has a remote** (`zexSites/codmailling-module-3-docs`, **private**, added 2026-07-30). Local
git protected the planning effort against an edit mistake; it did not protect it against disk failure,
and one `git remote add` closed that. Two notes that outlive the act: the repo is private because these
documents enumerate the security architecture — the credential-scrub mechanism, the two-vocabulary
envelope, the KMS-decrypt boundary — and a public planning repo is a free map of them; and this remote
is the **planning** remote, not module 3's code remote, so §1.2's merge step below still applies
unchanged. What the remote does change is the *price* of option (a).

**This makes the merge slightly more explicit, not harder.** The plan documents must be carried into
the forked tree, never overwritten by it, and there are two ways to do that:

| | How | Cost |
|---|---|---|
| **(a) Copy the working files** into the fresh clone and commit them there; discard the pre-fork repo | Simple, and the fork's history stays single-rooted. Loses the pre-fork commit history of the planning docs. | The planning history is small and its content is fully preserved in the files. |
| **(b) `git remote add` the pre-fork repo and merge it** as a second root | Preserves how the plans evolved. | Produces a two-root history, so `git merge-base` against the fork point behaves surprisingly — which is exactly the tool §5's shared-code manifest depends on. |

**(a) is recommended**, for the reason in the row: §5's manifest verification (`git merge-base`,
`git diff <fork-point>..HEAD`, `git log <fork-point>..HEAD`) is load-bearing, and an unrelated second
root degrades it to save history that the files themselves already carry.

**The remote makes (a) cheaper, which is worth stating because it removes (a)'s only real cost.** (a)'s
objection was that discarding the pre-fork repo loses how the plans evolved — the rulings, the
corrections, the reasoning in the commit messages. That history now lives at
`zexSites/codmailling-module-3-docs` and survives the discard, so (a) costs the fork nothing beyond a
pointer. The instruction is therefore: copy the working files into the fresh clone, commit them there,
and leave the planning history on the remote. Do not keep the pre-fork `.git` on disk merely to preserve
history it no longer solely holds — that is how a stray nested `.git` ends up in the forked tree.

Either way this is still **the only merge in the phase**, and sequencing it is part of the fork step.
One thing is now non-negotiable: the pre-fork repo's `.git` must **not** end up inside the forked tree
(a nested repo is the same trap §1.2 already flags for
`Docs/history/mailflow-worker (Sender Inspiration)/`).

### 1.3 Recording the fork point

A new document, `Docs/fork-point.md`, committed as the **first commit after the fork** and never
rewritten:

- Module 2 repo identity (path, and the fact that it had no remote at fork time)
- The full 40-char SHA, its `git log -1 --format=fuller` output verbatim, and the tag name applied
  in module 2
- Which module-2 phase state it represents, stated honestly: *"Phase 10 close (`d977e16`) plus the
  Stages 1–12 pre-deploy walk"* — not "Phase 10"
- The Gate A evidence: harness result, the six gate results, `sender-migrate status`, and the
  clean-slate reset, each with the observed numbers (§7)
- The known limits of that evidence, carried from the build plan's *Known and accepted*: module 2
  has **never sent through a real provider** — the entire baseline is Mailpit-green. Green here
  means "the fork is intact", never "the code is provider-validated".
- The one-way divergence statement (§5)

---

## 2. Rename and repo identity

Every row is a **mechanical** rename applied in Gate B. Proposed values assume the module slug
`byosmtp`; the slug itself is **M3-P0-OD-3** and every row below inherits that ruling.

### 2.1 Repo and build identity

| Surface | Module 2 (verified) | Module 3 | Where |
|---|---|---|---|
| Compose project name | `sender-module` | `byosmtp-module` | `docker-compose.yml:6` |
| Go module — Brain | `github.com/sender-module/brain` | `github.com/byosmtp-module/brain` | `services/brain/go.mod:1` |
| Go module — migration runner | `github.com/sender-module/sender-migrate` | `github.com/byosmtp-module/byosmtp-migrate` | `tools/sender-migrate/go.mod:1` |
| Go module — box provisioner | `github.com/sender-module/provision-box` | `github.com/byosmtp-module/provision-box` | `tools/provision-box/go.mod:1` |
| Go module — harness | `github.com/sender-module/harness` | `github.com/byosmtp-module/harness` | `tests/harness/go.mod:1` |
| npm package | `@sender-module/box` | `@byosmtp-module/box` | `services/box/package.json:2` |
| Import statements | 59 references across 48 files | rewritten | all four Go modules |
| Runner binary + dir | `sender-migrate` (`tools/sender-migrate/`) | `byosmtp-migrate` (`tools/byosmtp-migrate/`) | dir, `go.mod`, `docker-compose.yml:30–42`, `tests/harness/run.sh`, CI lint paths |
| Dev CA CN | `sender-dev-ca` | `byosmtp-dev-ca` | `tools/gen-certs.sh:55` |
| systemd units | `sender-brain.service`, `sender-box.service` | `byosmtp-brain.service`, `byosmtp-box.service` | `Docs/install-and-deploy.md:700,736` |
| CI workflow name | `real-infra` | unchanged (generic) | `.github/workflows/real-infra.yml:5` |
| CODEOWNERS handles | `@sender-arch`, `@platform-db` | see **M3-P0-OD-8** | `CODEOWNERS` |

### 2.2 Database identity — the load-bearing renames

Both modules are designed to run against the **shared platform Postgres** (module 2
`architecture-overview.md:22`, `install-and-deploy.md:29`). Every identifier below is therefore a
genuine collision risk, not a cosmetic one.

| Surface | Module 2 (verified) | Module 3 | Where |
|---|---|---|---|
| **Schema** | `sender` | `byosmtp` | `sender_0000_bootstrap.up.sql:26`; **1,097 schema-qualified references across 167 files** |
| Schema owner role (NOLOGIN) | `sender_owner` | `byosmtp_owner` | `sender_0000_bootstrap.up.sql:15–16` |
| App login role | `sender_module` | `byosmtp_module` | `sender_0000_bootstrap.up.sql:20–21`, `sender_0002_create_role.up.sql:12` |
| Migration tracking table | `sender.schema_migrations` | `byosmtp.schema_migrations` | `sender_0000_bootstrap.up.sql:31` |
| Migration file prefix | `sender_NNNN_*.{up,down}.sql` (50 files, `0000`–`0024`) | **M3-P0-OD-4** — defer to Phase 1 | `db/migrations/` |
| Enum types (7) | `sender.job_state`, `error_class`, `pool_kind`, `routing_strategy`, `box_status`, `event_actor`, `pause_reason` | schema-qualifier only; **type names unchanged** | `sender_0003_create_enums.up.sql:7–63` |
| Platform DB name | `platform` | **unchanged** — the external platform's DB | `docker-compose.yml:11` |
| Platform tables | `public.users`, `public.job_items` | **unchanged** — referenced, not owned | `sender_0000_bootstrap.up.sql:49–50` |
| Harness scratch DBs | `sender_migtest`, `sender_template`, `_p2`, `_p3` | `byosmtp_migtest`, `byosmtp_template*` | `tests/harness/run.sh:34,38,57,59` |
| `search_path` | **never set** — code fully qualifies | no action | — |

**Advisory-lock keys.** Module 2 reserved the entire `9_000_000_000–9_999_999_999` band from the
platform owner (`Docs/advisory-locks.md:7`, module 2's OD-10 / **module-2 pre-live gate G2**). Module 3
sharing that instance needs a non-overlapping band with the same three-key structure:

> **Label disambiguation, added 2026-07-31.** "G2" is now overloaded across the plan set and the two
> meanings are unrelated. **Module 2's G2** is a *pre-live gate* — a platform-owner sign-off in
> `Docs/history/module-2/pre-live-gates.md`, ticked on the go-live checklist. **Module 3's G2** is a *build
> gate* — the middle of the merged Phase 1+2 sequence (G1 strip pool logic → G2 drop pool schema →
> G3 add module 3's tables), one commit with its own green run. Everywhere else in this plan set a
> bare `G1`/`G2`/`G3` means module 3's build gates; the two references in **this** document mean
> module 2's, and are now qualified as `module-2 pre-live gate G2` at both sites. Note these are also
> distinct from Phase 0's own **Gate A / Gate B** (fork, then rename), which is a third sequence.

| Consumer | Module 2 | Module 3 (proposed) |
|---|---|---|
| Migration runner | `9_100_000_001` (`tools/sender-migrate/main.go:26`) | `8_100_000_001` |
| Brain singleton guard | `9_200_000_001` (`internal/singleton`) | `8_200_000_001` |
| Config-write serializer | `9_300_000_001` (`internal/adminapi`) | `8_300_000_001` |
| Per-user campaign lock | `hashtext('sender.campaign_start:' \|\| user_id)` | `hashtext('byosmtp.campaign_start:' \|\| user_id)` — dynamic, band-exempt by construction |

Band reservation is **M3-P0-OD-5**. A `Docs/advisory-locks.md` carrying module 2's band verbatim
is worse than no registry: it documents a reservation module 3 does not hold and would collide with.

### 2.3 Runtime identity

| Surface | Module 2 (verified) | Module 3 |
|---|---|---|
| Compose service keys / DNS aliases | `postgres`, `migrate`, `brain`, `box`, `box2`, `smtp-sink` | **unchanged** — architectural roles, namespaced by the compose project name |
| `container_name:` / `hostname:` / named volumes | **none set anywhere** | no action |
| Container OS users | `brain` (uid 10001), `box` (uid 10002) | unchanged |
| Cert leaf CNs/SANs | `brain`, `dev-box-1`, `smtp-sink` | unchanged — transport identity, not authz (`docker-compose.yml:107–109`) |
| Cert filenames | `secrets/dev/certs/{ca,brain,box,sink}.{crt,key}` | unchanged — component role, not module |
| Env prefix `SENDER_` | `SENDER_MODULE_PASSWORD`, `SENDER_ALERT_WEBHOOK_URL` | `BYOSMTP_MODULE_PASSWORD`, `BYOSMTP_ALERT_WEBHOOK_URL` |
| Env prefixes `BRAIN_`, `BOX_`, `MTLS_`, `KMS_`, `DEV_`, `MIGRATE_`, `PLATFORM_`, `ADMIN_`, `POSTGRES_`, `DATABASE_`, `TEST_` | component- or role-scoped, not module-scoped | **unchanged** |
| HTTP routes `/v1/*`, `/healthz`, `/readyz` | API-versioned | **unchanged**; module 3's extension routes are namespaced (build plan, *Metering and contract*) |
| Log topic prefixes `dispatch:`, `outcome:`, `reclaim:`, `retention:`, `shutdown:`, `singleton:` | component-scoped | unchanged |
| `sender.config` **key names** | 80-line manifest | key names unchanged; only the schema qualifier moves |
| Metric name prefixes | none implemented | no action |

**Do not rename** — recorded so the rename pass does not over-reach: the `platform` DB name,
`public.users`/`public.job_items`, `deploy/pg-init/00-platform-fixtures.sql`, the 7 enum *type*
names, all config *key* names, `/v1` route paths, compose service keys, cert component filenames,
the `variation_*` feature dials and the injected `User-Agent` header.

### 2.4 The collision the compose project name does not solve

Compose namespaces networks and containers by project name, so `sender-module` and `byosmtp-module`
coexist. **Host port bindings do not namespace.** Both stacks bind, identically:

`5432` (postgres) · `8080` (Brain health + Domain A + Domain B) · `8443` (boxlink mTLS) ·
`8444` (boxlink send-stream mTLS) · `8025` (Mailpit UI)
— `docker-compose.yml:17,79–82,191–193`

Two stacks up simultaneously on one dev host: the second fails to bind, or worse, a harness run
against `localhost:8080` reaches the **other module's Brain** and produces a confidently wrong
verdict. Every harness gate addresses the stack through host-mapped `localhost` ports. Resolution
is **M3-P0-OD-6**.

---

## 3. CI and harness inheritance

Module 2's harness and gate discipline are `phase-0-groundwork.md` §4 (harness topology, real
Postgres isolation, Mailpit, anti-vacuity, CI wiring) and §5 (migration tooling). **Not
re-specified here.** What Phase 0 owes is the confirmation that they run against module 3's own
stack, and an honest classification of each gate.

### 3.1 Entry points — inherited unchanged

`Makefile` (delegator) → `scripts/dev.sh {certs,build,up,up2,seed,test,down,reset,logs,ps,help}`
→ `tests/harness/run.sh` (10 suites) · `docker-compose.yml` + `tests/harness/compose.test.yml`
(tmpfs Postgres, `fsync=off`) · `.github/workflows/real-infra.yml` (single job `behavioral`,
`on: [push, pull_request]`, required status check).

The harness needs no external secrets: `scripts/dev.sh certs` mints the CA, leaves, dev KMS key,
box tokens and admin/platform keys in-job, and writes `.env`. **The fork therefore has no secret
to provision before Gate A** — it self-provisions, which is exactly why Gate A can run on day one.
Confirmed by `.github/workflows/real-infra.yml:27–31`: no `secrets.*` reference anywhere.

### 3.2 The six blocking gates

| Gate | Carries over | Rename touch | Subject matter |
|---|---|---|---|
| Go lint (`gofmt -l` + `go vet` ×4 modules) | unchanged | **yes** — hardcodes `services/brain tools/sender-migrate tools/provision-box tests/harness`; the runner dir rename (§2.1) moves one path | generic |
| `config_drift.sh` | unchanged | **yes** — `sender.config`, DB `platform` | mechanism generic; **key set is module 2's** |
| `contract_drift.sh` | unchanged | **yes** — route table, box id | **module-2-specific assertions** (below) |
| `migration_lint.sh` | unchanged | **yes** — `sender_` prefix, `sender.schema_migrations` | chain-shape; generic given a chain |
| `config_consumption.sh` | unchanged | no repo-name dependency | mechanism generic; key set module 2's |
| `drain_grace.sh` | unchanged | **yes** — service key `brain` in the compose scan, DB `platform` | generic |
| `no_secret_leak.sh` | unchanged | **yes** — reads `DEV_BOX_TOKEN`/`DEV_BOX2_TOKEN`, `--profile multibox` | generic |

Also inherited: `tests/harness/load_e11.sh` — the single-Brain throughput-ceiling load gate,
`workflow_dispatch`-only and **never wired into CI** (`real-infra.yml:70–72`). It carries over
unrun. Module 3 re-measures at Phase 8 (build plan: module 3's load shape "will not match module
2's 370/min").

### 3.3 Gates whose *subject matter* is module-2-only

These pass unchanged at Gate A and Gate B, and **must**, because Phase 0 deletes nothing. They
break at Phase 1/2 by design. Recorded here so that breakage is expected, not investigated:

| Gate / suite | The module-2 assertion | Breaks at |
|---|---|---|
| `contract_drift.sh:53–56` | asserts `GET/POST /v1/admin/pools`, `GET/PATCH/DELETE /v1/admin/pools/{id}` are served and 401-gated | Phase 2 |
| `contract_drift.sh:47–52,100–101` | operator-only account lifecycle: `quarantine`/`unquarantine`/`disable`/`enable`/`pause`/`unpause` | Phase 3/5 — module 3's un-quarantine is user self-serve |
| `load_e11.sh:39–41` | `POST /v1/admin/pools {"kind":"shared","name":"e11-load-pool"}`, then sets `default_shared_pool_id` | Phase 2 |
| `tests/harness/phase3_send.sh:66–75` | the **live send-path suite** creates a shared pool and sets `default_shared_pool_id` before any job dispatches | Phase 2 |
| `tests/harness/phase8_variation.sh:257` (comment) | reuses `phase3_send.sh`'s pool fixtures | Phase 2 |
| `phase1_grants_invariant_test.go`, `phase1_crypto_roles_test.go` | grants/RLS asserted on `sender.pools`, `accounts`, `jobs`, `send_logs` | Phase 1 |
| `go_suite services/brain/internal/adminapi` (`run.sh:125`) | pool owner/cycle/RESTRICT + `fallbackCycleExists` behaviour | Phase 2 |
| `migration_lint.sh:67–76` | couples the chain head to the `wantChain` literal in `tools/sender-migrate/runner_integration_test.go` | Phase 1 (chain renumber) |

**Consequence worth stating plainly:** module 3's green baseline is *pool-dependent*. The live send
path cannot dispatch a job without a shared pool at the fork point. Phase 2's deletion phase
necessarily rewrites these fixtures, and the build plan already owns that ("the double-booking test
is **rewritten** to reflect the new reality"). Phase 0 must not pre-empt it.

### 3.4 Genuinely module-3-specific gates — noted, not built

Owned by later phases. Listed so Phase 0 does not accidentally scaffold them:

| Gate | Asserts | Owner |
|---|---|---|
| deleted-account-never-routed | a deleted account inside its retention window is never selected for a send; the purge genuinely removes the secret | Phase 3 |
| From-gate / DNS | `p=reject` blocks, `p=none` warns, aligned allows, lookup failure fails closed, no mid-campaign re-block | Phase 4 |
| user-facing credential scrub | an echoed auth string in a provider response is scrubbed **plaintext and base64** before reaching the user | Phase 7 (extends `no_secret_leak.sh`'s already-fixed base64 lesson, `9daa622`) |
| per-user override isolation | an override applies to exactly one user; users cannot reach pacing or concurrency by any route | Phase 6 |
| notification digest | ten simultaneous failures produce one digest; immediate-severity is not held | Phase 7 |
| structural no-contention | with per-user accounts and one campaign per user, contention is impossible — *proven*, not assumed | Phase 2 |

---

## 4. CLAUDE.md adaptation

Module 2's `CLAUDE.md` (11 numbered rules + the DoD pointer) is the `/init` anchor and carries over.
Rule 8 governs this adaptation: it is **one deliberate, reviewed change**, and newly locked
invariants are appended, never quietly rewritten.

### 4.1 Untouched — verbatim, no wording change

| Rule | Note |
|---|---|
| **1. Config not code** | Including both config-ordering invariants, `config_ordering_violation`, and the migration-runner bootstrap-timeout exception. Identifier references follow §2.2 mechanically; the *rule* does not change. The "50-key"/"Phase 9 §4" pointers repoint to module 3's own reference (§4.3). |
| **2. Flag, don't silently fix** | Verbatim. Ledger paths repoint to module 3's own ledger + ADR dir. |
| **3. Real behavioral tests only** | Verbatim. `tests/harness/` is inherited as the home. |
| **4. Documentation is part of every change** | Verbatim. |
| **5. Contracts, errors, and honesty** | The two-vocabulary error rule, the alert-webhook clause, and "never invent an outcome the box did not report" are **verbatim**. The enum sentence changes — see §4.2. |
| **6. Load-bearing security properties** | Verbatim, all nine bullets: Brain sole decryptor, memory-only + zeroize, box has no DB/KMS, mTLS, STARTTLS-before-AUTH, registration-token authority, auth-domain separation, `sender_owner` non-login ownership so `FORCE RLS` enforces. |
| **7. Autonomous-subsystem safety controls** | Verbatim, including `autoquarantine_mode` default **`shadow`** — which is exactly what module 3 needs ("Auto-quarantine ships in shadow at launch") — and the Brain singleton clause **as amended by ADR-0011** (pause + bounded re-acquire on lock loss). |
| **8. How this file is maintained** | Verbatim. Governs this very change. |
| **9. Account→job lock ordering** | Verbatim, including both 2026-07-26 Postgres sub-points. This is a module-3 standing invariant (build plan). ADR-0010 comes with it. |
| **10. Anti-vacuity for concurrency tests** | Verbatim, including the `pg_stat_activity` scoping addition. |
| **11. Review gates run SEQUENTIALLY** | Verbatim. |
| **DoD pointer** | Nine items unchanged; the link repoints to module 3's own copy. |

### 4.2 Changes — the module-specific surface only

1. **Title line.** `# CLAUDE.md — Operating Rules (Module 2, Email Sender)` → module 3's identity.
2. **Status block.** Module 2's "all eleven phases … closed" block is replaced by a **fork status**
   block: forked from module 2 at `<SHA>`, Phase 0 baseline green, Phases 1–8 pending. Module 3 is
   under active development; a "sealed" status line would be false on day one.
3. **Rule 5, the enum sentence.** Module 2 names **seven** enums "additive-only forever", including
   `pool_kind`. Module 3's Phase 1 drops pools. The rule as written would forbid its own Phase 1.
   The resolution is a **ruling, not an edit** — **M3-P0-OD-7**. Phase 0 flags it and leaves the
   rule verbatim; Phase 1 owns the amendment.
4. **The design line — new, and the only genuinely new content:**
   > **Users control their own assets. The operator controls the shared substrate.**

   With the build plan's operative half: users set their own daily limits, choose their own From
   address, restart their own paused accounts, see the full technical error text, and are notified
   about everything actionable. The operator controls queue depth, per-user suspension, and
   **pacing** — the one dial that could damage shared infrastructure, deliberately kept out of
   users' hands. *"When a new question arises, answer it with that line."*
5. **Inherited-unchanged list.** The build plan's *"Inherited from module 2 — unchanged"* 13 bullets,
   transcribed, with the standing instruction: **fork it, keep it, do not redesign it.**
6. **Deleted-from-module-2 list.** The 5 bullets (shared pool, fallback chains + `MAX_FALLBACK_HOPS`,
   cross-user contention, operator-owned accounts, pools as a data-model concept) — recorded in
   Phase 0 as **scope**, deleted in Phases 1–2. Phase 0 deletes nothing.
7. **Standing invariants.** The build plan's 6 carried-across invariants, verbatim. Five restate
   Rules 9, 1, and 6; two are worth their own line because they are cheap to violate: *"a
   Brain-declared reclaim never scores the account"*, *"bounce never penalises the account"*,
   *"wait-vs-fail ordering: empty and self-recovering chains WAIT, evaluated before the fail
   branch"*, and *"any new table needs explicit grants"* (ADR-0008's trap: default privileges do
   not fire for admin-created-then-reowned tables).

### 4.3 Documents the rules point at

`CLAUDE.md` references documents by path. Each pointer resolves to a module-3 artifact or a module-2
provenance artifact — never silently to a stale module-2 living doc:

| Pointer | Resolution |
|---|---|
| Definition of Done | module 3's own copy, inherited verbatim from `Docs/history/definition-of-done.md` (nine items). Its DoD-4/DoD-7 text names `sender.config`, `sender.schema_migrations`, the 7 enums, and the `sender_0001..0020` chain — identifier updates follow §2.2; the **gate** does not change. |
| Open Decisions + ADRs | **fresh module-3 ledger**, starting with this document's register. Module 2's 156 KB ledger is provenance (§5, **M3-P0-OD-9**). |
| Config reference / API contract | inherited files, renamed identifiers, kept green by the inherited `config_drift` / `contract_drift` gates. |
| `Docs/advisory-locks.md` | **must** be rewritten to module 3's band at Gate B (§2.2) — inheriting module 2's band verbatim documents a reservation module 3 does not hold. |

---

## 5. Shared-code manifest

A new document, `Docs/shared-code-manifest.md`. **Its purpose is bug-checking, not synchronisation.**

### 5.1 What it is for

A bug found in either codebase can be checked against the other, because the manifest says which
files are still the same code. That is the whole claim.

### 5.2 What it explicitly is not

Quoting the build plan's *Known and accepted* rather than re-deriving it:

> **Back-porting will not really happen.** Module 2 is finished; module 3 will be under active
> development. Fixes will flow one direction and module 2 will drift. Reconcile at the all-modules
> polish pass rather than relying on the manifest being maintained.

So the manifest states, in its own opening lines:

- Flow is **module 3 → module 2 only**, and in practice **rarely**. Module 2 is sealed.
- Reconciliation happens at the **all-modules polish pass**, not continuously.
- This is **a checking aid, not a sync promise.** A file listed as "inherited verbatim" means *it
  was verbatim at the fork point* — the manifest is a starting position plus a mechanical way to
  ask what has changed since, never an assertion about today.
- Module 2 has never sent through a real provider. Provider-level bugs exist in **two** codebases
  and will be found once. Accepted deliberately (build plan, *Known and accepted*).

### 5.3 Structure

One row per inherited area: **area · paths · inherited state at fork · divergence expected at phase
N · module-2 provenance reference**. Areas, from the verified inventory:

| Area | Paths | Divergence expected |
|---|---|---|
| Box service (SMTP session, STARTTLS-before-AUTH, memory-only creds + zeroize, outcome buffering) | `services/box/` | ~~none planned — inherited whole~~ → **Phase 7, in `smtp.js`** (see the note below) |
| Content variation (12 toggles, all-off byte-identical) | `services/box/src/variation*.js`, `tests/harness/phase8_variation.sh` | none — build plan: inherited whole |
| Box↔Brain channel (mTLS, register/heartbeat, send stream, persist-ack) | `internal/boxlink/`, `internal/boxproto/`, `src/boxlink/` | none planned |
| Credential encryption (AES-256-GCM, AAD-bound, key-id label check) | `internal/credential/`, `internal/kms/` | Phase 3 (third-party secrets, break-glass read) |
| Job lifecycle, append-only event log, actor GUC | `db/migrations/sender_0003..0008`, **`sender_0022`** (see note), `internal/dispatch/` | Phase 1 (additive); **Phase 7 replaces `fn_job_events_on_state_change`'s body** |
| Idempotency (`provider_message_id`, `attempt_token`) | `sender_0020`, `internal/dispatch/` | none planned |
| Error taxonomy, requeue-only re-dispatch | `internal/dispatch/classify.go` | Phase 5 (dual quarantine triggers) |
| Pacing (per-lane random delay) | `internal/dispatch/` | Phase 6 (per-user tuning) |
| Reclaim loop, heartbeat dead-box detection, lease expiry, startup rescan | `internal/dispatch/reclaim*.go`, `rescan*.go` | none planned |
| Singleton guard **incl. ADR-0011 pause + re-acquire** | `internal/singleton/`, `sender_0023` | none planned |
| Dispatcher, routing, eligibility | `internal/dispatch/eligibility.go`, `dispatch.go` | **Phases 1, 2, 6 — heavy** |
| Admin + platform API surfaces | `internal/adminapi/`, `internal/platformapi/` | Phases 3, 5, 6, 7 |
| Migration runner + preflight | `tools/byosmtp-migrate/`, `db/preflight/` | rename only |
| Harness + 6 CI gates | `tests/harness/`, `.github/workflows/` | Phases 1–7 per §3.3 |
| Cert / secret / provisioning tooling | `tools/gen-certs.sh`, `gen-secrets.sh`, `provision-box/` | rename only |
| Local dev loop | `scripts/dev.sh`, `Makefile`, `docker-compose.yml` | rename + §2.4 |

**Two forecast corrections, made 2026-07-31 rather than left for Phase 8 to discover.** The table
above is a *forecast*, and Phase 8 §7.3 replaces it with an observation — but where a ruling has
ALREADY falsified a row, leaving the row as written would have Phase 8 "discover" something this
document could have stated. Both corrected rows are marked inline above:

1. **The box is no longer forecast as untouched.** M3-P7-OD-2 (AUTH PLAIN in scope) and M3-P7-OD-8
   (`scrubSecret` throws on a zeroed buffer) are both changes to `services/box/src/smtp.js`. The
   consequence is the one this manifest exists to deliver: **a module 2 `smtp.js` bug no longer
   applies to module 3 unchanged.** Note the direction, because it is not symmetric — module 3 is
   strictly *more* fail-closed on the credential path.
2. **A migration-resident function body is not covered by a file-based row.** Phase 7's `0046`
   (renumbered from `0044` by M3-P5-OD-7's cascade, then from `0045` by M3-P6-OD-6's cascade) does
   a `CREATE OR REPLACE` on
   `fn_job_events_on_state_change` (M3-P7-OD-6). A function's live
   definition is the **last** `CREATE OR REPLACE` in the chain — `sender_0022`, not the `sender_0008`
   that created it — so `git diff` on `sender_0008` returns empty and still tells you nothing about
   whether the function diverged. For SQL-resident logic the manifest row must name the **defining**
   migration; the row above now does. Phase 8 §7.3 carries this as a stated limit of the axis.

### 5.4 How a check is actually run

The manifest names the commands, so the discipline is mechanical rather than a memory exercise:

- `git merge-base module3-fork-point HEAD` — confirm shared ancestry still resolves
- `git diff <fork-point>..HEAD -- <path>` — has module 3 touched this file since the fork? Empty
  diff = still verbatim = a module-2 bug applies here unchanged.
- `git log <fork-point>..HEAD -- <path>` — if not verbatim, what changed and in which phase
- The reverse, run in module 2 against its own HEAD, catches the case where module 2 moved after the
  fork (it should not, but "should not" is not "cannot")

**This is why §1.2 requires history-preserving clone.** Without shared ancestry every row degrades
to prose that cannot be verified.

### 5.5 Provenance carried across

Module 2's `Docs/history/` holds 12 locked phase specs, 13 ADRs, the 156 KB decision ledger, and
the 183 KB build record. Several are load-bearing for **inherited** code — ADR-0008 (grants trap),
ADR-0010 (lock ordering, cited by CLAUDE.md Rule 9), ADR-0011 (singleton lock loss), ADR-0012
(`job_events` FK carve-out). Code comments in inherited files cite these by number. Scope of
inheritance is **M3-P0-OD-9**. Note that module 2's `Docs/history/README.md` already warns that
paths inside those documents predate the `history/` move — a second fork adds a second layer of the
same drift, which is an argument for marking rather than rewriting.

---

## 6. What Phase 0 deliberately does not touch

Explicit, so no ambiguity survives into execution. Phase 0 changes **identity and nothing else**.

| Not touched | Owner |
|---|---|
| Any schema change — no `ALTER`, no new table, no new column, no new enum value, no new config key | Phase 1 |
| The pools table, pool assignment, `fallback_pool_id`, fallback columns, shared-pool config keys — **nothing is dropped** | Phase 1 |
| Pool resolution, fallback chains, `MAX_FALLBACK_HOPS`, cross-user contention handling | Phase 2 |
| The account ownership model — accounts stay pool-owned, uniqueness stays per-pool | Phase 1 |
| Account CRUD, verify-on-add, test button, bulk add, deletion + retention | Phase 3 |
| The From gate — no SPF, no DMARC, no DNS lookup, no resolver config | Phase 4 |
| Quarantine triggers, un-quarantine self-serve + rate limit, credential-update auto-clear | Phase 5 |
| Campaign size cap, per-user queue cap, campaign expiry, per-user suspend | Phase 5 |
| Per-user config overrides, routing strategy as a user setting | Phase 6 |
| User-facing error surfacing, per-account attribution, notifications, `quote`-as-feasibility, `describe` extension routes, operator throttle health | Phase 7 |
| Any new config key, any new migration, any new API route | Phases 1–7 |
| The migration chain — inherited `0000`–`0024` applied **as-is**; not renumbered, not squashed, not re-prefixed | Phase 1 (**M3-P0-OD-4**) |
| Rewriting any pool-dependent test or gate (§3.3) — they must stay green as inherited | Phases 1–2 |
| Amending CLAUDE.md Rule 5's enum list | Phase 1 (**M3-P0-OD-7**) |
| Re-measuring the throughput ceiling; `load_e11.sh` carries over unrun | Phase 8 |
| Operational documentation rewritten for module 3 (install, runbook, DR, security posture) | Phase 8 |
| Sending through a real provider | out of scope for the whole build until the all-modules test pass |

Corollary: **a red harness at Gate A or Gate B is never fixed by changing behaviour.** It is a fork
defect, an environment defect, or a missed rename. If a genuine module-2 bug surfaces, it is
flagged under Rule 2 and assigned to the phase that owns it — Phase 0 does not fix code.

---

## 7. Exit criteria

From the build plan's Phase 0 exit criteria, made verifiable. Every numeric value is **observed and
recorded at the fork point**, not predicted — module 2's last recorded whole-harness figure was
`58 passed, 0 failed` at `bc9b74e`, and commits after it added tests, so the baseline number is
whatever Gate A produces. Recording it is the point; asserting it in advance would be inventing
evidence.

### Gate A — inheritance (byte-identical tree, zero edits)

- [ ] `git status --porcelain` empty; `git rev-parse HEAD` equals the recorded fork-point SHA
- [ ] `bash scripts/dev.sh certs` mints CA + leaves + dev KMS key + box tokens + admin/platform keys and writes `.env` — no external secret needed
- [ ] `bash scripts/dev.sh up` brings up `postgres → migrate → brain → box → smtp-sink`, all healthchecks satisfied (`up -d --wait` returns 0)
- [ ] `bash scripts/dev.sh test` — **all 10 suites green, zero failures, zero skips.** Skips confirmed **by reading the log**, not by an absent-`SKIP` grep (module 2's Stage 1 recorded exactly this trap: the only `SKIP` string was a test *name*)
- [ ] All **six** gates green, run individually: `config_drift`, `contract_drift`, `migration_lint`, `config_consumption`, `drain_grace`, `no_secret_leak`
- [ ] `gofmt -l services/brain tools/sender-migrate tools/provision-box tests/harness` empty; `go vet ./...` clean in all four modules
- [ ] Box Node suite green (`node --test "test/*.test.mjs"`)
- [ ] **Clean-slate reset from an empty volume:** `bash scripts/dev.sh reset` (down `-v` → postgres → `migrate` → seed → full stack) completes, then `byosmtp-migrate status` reports **25 applied, 0 pending** (`0000`–`0024`) and the full harness is re-run green *from that clean slate* — not from a warm stack
- [ ] Both boxes `active` after reset, each registered exactly **once** (`--profile multibox`), zero eviction churn
- [ ] `docker compose config` resolves with no warnings on the base + `tests/harness/compose.test.yml` override
- [ ] Gate A results written into `Docs/fork-point.md` **with the observed numbers**, and committed

### Gate B — identity (rename applied, nothing else)

- [ ] Every §2.1–§2.3 row applied; `git diff <fork-point>..HEAD` contains **only** identity changes — no behaviour change is present in the diff, verified by reading it
- [ ] Residual-literal sweep clean: no `sender-module`, `sender_owner`, `sender_module`, `github.com/sender-module`, `@sender-module`, `sender-dev-ca`, or `sender_migtest`/`sender_template` outside `Docs/history/` (module-2 provenance, deliberately left as-written) and outside migration filenames (**M3-P0-OD-4**)
- [ ] Schema sweep: 1,097 `byosmtp.<object>` references resolve; zero references to schema `sender`
- [ ] Every Gate A checkbox re-run and green on the renamed tree, **including the clean-slate reset** — a rename that breaks migration application must be caught here, not at Phase 1
- [ ] `Docs/advisory-locks.md` rewritten to module 3's band; no module-2 key literal remains in code or docs
- [ ] `CLAUDE.md` adapted per §4: rules 1–4, 6–11 and the DoD verbatim; title, status block, design line, inherited-unchanged list, deleted list, and standing invariants updated; Rule 5's enum sentence left verbatim with **M3-P0-OD-7** flagged
- [ ] `Docs/fork-point.md` and `Docs/shared-code-manifest.md` committed
- [ ] Module 3's Open Decisions ledger exists and contains this register
- [ ] §6's not-touched list verified against the diff: zero migrations added, zero config keys seeded, zero routes added, zero tests rewritten
- [ ] Module 2 tagged `module3-fork-point`; the tag resolves to the recorded SHA
- [ ] **DoD tick-off recorded** for Phase 0 against all nine items, with explicit "not applicable, because…" wherever there is nothing to bind to — a ✅ meaning "N/A" is only honest if it says which (module 2's DoD preamble). Phase 0 seeds no config key (DoD-4), adds no tuning constant (DoD-5), and adds no lock (DoD-2's race-test clause).

---

## 8. Open decisions

> **Consolidated queue: [`Docs/open-decisions.md`](open-decisions.md).** That file is where these are
> ruled from — it records which of them a later phase's ruling already closed, and which bucket
> (BLOCKS / RULE / ACCEPT) each remaining one sits in. This register stays authoritative for full
> context.

**All eleven are RULED as of 2026-07-31.** Nine were approved as recommended, two were ruled where this
doc offered a choice or no recommendation, and two (OD-4, OD-7) were closed by Phase 1's rulings rather
than separately. Nothing here is still waiting.

| # | Decision needed | Why it cannot be settled here | Recommendation |
|---|---|---|---|
| **M3-P0-OD-1** ✅ **RULED-ACCEPTED 2026-07-31** | The build plan lists "renamed services" under Phase 0 *Builds* and "harness green **before a single line is changed**" under *Exit criteria*. A rename is a change. Adopt the two-gate reading (§0)? | Reconciling two clauses of the authoritative plan is a ruling, not an interpretation to make quietly. | **Adopt.** Gate A isolates fork defects from rename defects; one combined gate produces an ambiguous verdict with two unrelated remedies. **Approved as recommended — the two-gate reading is now the plan's language everywhere** (§0; the build plan's Phase 0 entry rewritten to say Gate A / Gate B rather than restating the contradiction) |
| **M3-P0-OD-2** ✅ **RULED 2026-07-31** | Which commit is the fork point. HEAD `21b65e6` is 36 commits past the formal Phase 10 close `d977e16`; those commits contain the Stage 1–12 live walk and fixes for a HIGH singleton defect, the `job_events` delete wedge, a base64 credential-leak blind spot, and the drain SIGKILL. | The plan says "a known-good post-Phase-10 commit" and both qualify. Forking at `d977e16` knowingly imports fixed bugs — including one directly under Phase 3's account-deletion scope. | **HEAD `21b65e6…`.** Also tag it in module 2 (metadata-only; no tracked file changes, so Rule 8 is not engaged). **RULED: (a) `21b65e6`, tagged in module 2** — full SHA `21b65e65eba8f2cc16a84408bac6e018511ec46b`. §1.1 records one factual correction: `21b65e6` is HEAD~1, not HEAD (HEAD is the docs-only `d34e57a`); the explicit SHA governs, and the one-commit gap costs module 3 two inherited `future-work.md` entries that its own plans carry in more detail |
| **M3-P0-OD-3** ✅ **RULED 2026-07-31** | The module slug, from which every name in §2 derives: `byosmtp` (used throughout as a placeholder), or another. | The slug reaches the **schema name**, which becomes historical data the moment Phase 1 runs and is expensive to change afterwards. Deciding it silently locks it. | Pick before Gate B. Constraints: valid unquoted Postgres identifier, short (it prefixes 1,097 references), unmistakably distinct from `sender`, and not encoding "3" (module numbering is internal, not product identity). **RULED: `byosmtp`** — the placeholder becomes the name. It satisfies all four constraints, and it is now **permanent under Rule 5**: it fixes the schema, the owner and app roles, the migration tracking table, container names, the migration file prefix (M3-P0-OD-4/M3-P1-OD-2), and the `/v1/byosmtp/…` route prefix (M3-P3-OD-1, which therefore needed no independent choice) |
| **M3-P0-OD-4** ✅ **CLOSED-BY M3-P1-OD-2 2026-07-31** | Does Phase 0 re-prefix the migration files (`sender_NNNN` → `<slug>_NNNN`), or does Phase 1's "numbered fresh for this module" own it? | Re-prefixing at Gate B changes what `migration_lint` and the `wantChain` literal assert, and Phase 0's own exit criterion requires the **inherited** chain to apply from an empty volume. Both readings are defensible. | **Defer to Phase 1.** Phase 0 keeps `sender_NNNN` filenames while renaming schema/roles/tracking-table inside them. Phase 1 owns the chain — and is where "numbered fresh" is actually specified. Record the deferral explicitly so the residual `sender_` literal in §7's sweep is a known exception, not an oversight. **The deferral was accepted and Phase 1 took the question:** Phase 1 §1.1b rules the re-prefix and states it discharges this item, so it is closed here rather than separately ruled. Phase 0 keeps `sender_NNNN` filenames as recommended |
| **M3-P0-OD-5** ✅ **RULED-ACCEPTED 2026-07-31** | Module 3's advisory-lock band. Module 2 reserved the whole `9_000_000_000–9_999_999_999` space from the platform owner (module 2's OD-10 / **module-2 pre-live gate G2** — not module 3's build gate G2; see §2.2's disambiguation note). | The reservation is a **platform-wide** agreement, not a module-local choice; module 3 cannot grant itself a band. | Reserve `8_000_000_000–8_999_999_999` with mirrored keys `8_100_000_001` / `8_200_000_001` / `8_300_000_001`. Confirm with the platform owner as a pre-live gate (mirroring **module 2's** pre-live G2), not a build blocker. **Approved as recommended.** The platform-owner confirmation stays a **pre-live gate**, which means it is a go-live-checklist item (Phase 8 §11), not something a build phase can tick |
| **M3-P0-OD-6** ✅ **RULED-ACCEPTED 2026-07-31** | Host-port collision (§2.4): both stacks bind `5432`, `8080`, `8443`, `8444`, `8025`. Compose project names do not namespace host ports. | Affects both the dev loop and CI, and a wrong choice yields a harness that silently tests the *other* module's Brain — a confidently wrong green. | Make the host side of every mapping `.env`-driven with module-3 defaults offset (e.g. `+1000`: `6432`/`9080`/`9443`/`9444`/`9025`). Container-internal ports and service DNS names stay unchanged, so nothing inside the stack moves. Alternative — never run both stacks at once — is a discipline, not a mechanism. **Approved as recommended** |
| **M3-P0-OD-7** ✅ **CLOSED-BY M3-P1-OD-3 2026-07-31** | CLAUDE.md Rule 5 names **seven** enums "additive-only forever", including `pool_kind`, and says "never remove". Module 3's Phase 1 drops pools. Does the additive-only clock reset at module 3's own Phase 1 baseline (nothing deployed yet), leaving six enums? | This is a **locked contradiction** in the operating rules — precisely Rule 2's "never resolve on your own authority". The rule as inherited forbids module 3's own Phase 1. | Rule 5's guarantee protects *deployed* contracts against consumers. Module 3 has no deployed history at Phase 1, so the honest reading is a reset baseline: `pool_kind` never exists in module 3, the remaining six become additive-only-forever from Phase 1's chain. **Ruling needed before Phase 1**; Phase 0 leaves the text verbatim. Also touches DoD-7's "7 locked enums stay frozen". **Closed by M3-P1-OD-3's ruling**, which answers the identical question — does the additive-only clock reset at the fork — **once for all five removals** rather than per-item. Phase 0 still leaves the inherited text verbatim; the reset is Phase 1's to record |
| **M3-P0-OD-8** ✅ **RULED-ACCEPTED 2026-07-31, CONFIRMED INERT 2026-08-02** | `CODEOWNERS` handles `@sender-arch` and `@platform-db`, gating `db/migrations/`, `CLAUDE.md`, `tools/migrate/`, and `Docs/contracts/`. Do these teams exist for module 3? | A CODEOWNERS file naming nonexistent teams either blocks all merges or silently enforces nothing. Which of the two is a hosting-config fact. | Carry the file's *structure* verbatim (the change-control intent is inherited); confirm or rename the handles at Gate B. If the repo has no host/teams yet, record that CODEOWNERS is inert and why. **Approved as recommended** — the handle rename to `@byosmtp-arch`/`@platform-db` landed at Gate B, but Phase 6's phase-start mechanical landing check found the second half of the ruling ("confirm or record inert and why") was never actually done — a rename is not a confirmation. **Confirmed 2026-08-02: CODEOWNERS is inert, recorded here as required.** `origin` is `github.com/zexSites/codmailling-module-3`, owned by a personal GitHub **User** account, not an organization — GitHub Teams (and therefore `@org/team` CODEOWNERS handles) do not exist as a concept on a personal account. Checked directly: `@byosmtp-arch` is not a GitHub account at all (`404`); `@platform-db` happens to resolve to an unrelated real GitHub user with no connection to this project, so that handle is accidentally *valid-looking* but not the intended reviewer. Independently, CODEOWNERS enforcement requires branch protection, and `gh api repos/.../branches/main/protection` returns `403 Upgrade to GitHub Pro or make this repository public` — the feature is unavailable on this repo's current plan/visibility regardless of handle validity. Net: the file is present (preserves the structure/intent per the ruling) but currently enforces nothing; it becomes live only if this repo moves to an org with real teams and gets branch protection enabled. No action taken beyond recording this — changing hosting plan/visibility is an infrastructure decision, not a docs fix |
| **M3-P0-OD-9** ✅ **RULED-ACCEPTED 2026-07-31, LANDED 2026-08-02** | How much of module 2's `Docs/history/` comes across: 12 phase specs, 13 ADRs, the 156 KB decision ledger, the 183 KB build record. | ADRs 0008/0010/0011/0012 are load-bearing for **inherited** code and cited by number in inherited code comments and by CLAUDE.md Rule 9. Dropping them orphans those citations; copying everything imports module 2's build diary as if it were module 3's. | Carry the **ADRs** and the phase specs for inherited subsystems, in a clearly-marked `Docs/history/module-2/` with a README stating "module 2 provenance; not module 3's record; paths inside predate two moves". Do **not** carry the decision ledger or build record — reference them by module-2 path. Start a fresh module-3 ledger. **Approved as recommended** — and Phase 8 §3.1's `Docs/history/module-2/` contents are exactly what this admits, no more. **Found genuinely unbuilt by Phase 6's phase-start mechanical landing check** (a status word of RULED-ACCEPTED in the ledger is not evidence the artifact was ever built, CLAUDE.md Rule 11 — the directory held a byte-identical copy of module 2's ENTIRE `Docs/history/`, not the narrow admitted subset, and module 3's own fresh ledger at `Docs/open-decisions.md` had separately, correctly, already been started, so only the file-reorganization half was outstanding). **Landed 2026-08-02:** the 12 phase specs, `phase1-drafts/`, and the 13 ADRs (`decisions/0000`–`0012`) moved to `Docs/history/module-2/` (`git mv`, history preserved) with a README stating the required "module 2 provenance; not module 3's record; paths inside predate two moves" framing; module 2's `open-decisions.md` and `phase-build-record.md` were left at their original `Docs/history/` path (not moved into `module-2/`) specifically because inherited code cites them there by literal path, and rewriting the citation would be indistinguishable from carrying the ledger/build-record itself — the ruling's "reference them by module-2 path" is satisfied either way, since the path still resolves to module 2's own file. `Docs/history/definition-of-done.md` (CLAUDE.md's own cited DoD path) got special handling: module 2's original copy was preserved unchanged at `Docs/history/module-2/definition-of-done.md`, and a NEW module-3-vocabulary DoD (byosmtp.\*, Rule 5's amended additive-only framing, no fabricated per-phase tick-off history) now lives at the `Docs/history/definition-of-done.md` path CLAUDE.md actually points at. Every literal path citation to a moved file, across `Docs/`, `services/`, and `db/migrations/`, was swept and repointed to `Docs/history/module-2/...`; citations by ADR number alone were left untouched |
| **M3-P0-OD-10** ✅ **RULED-ACCEPTED 2026-07-31** | Module 2's `README.md:88` states "Five blocking CI gates" and its table lists five — CI has run **six** since `drain_grace` landed (module 2's own Stage 1 note corrected `architecture-overview.md` but not the README). Fix in the Gate B rename pass, or leave inherited docs untouched? | Phase 0's discipline is "identity changes only". A factual correction is not an identity change — but shipping a known-false doc as module 3's own README is worse. | Fix at Gate B and record it as a one-line exception to "identity only" in the fork-point document. Then flag it to module 2 as the **first entry** in the shared-code manifest's one-way flow — a live, low-risk exercise of the discipline. **Approved as recommended, with one correction to the wording:** it is the manifest's **second** entry, not its first. Two documents each claimed "first" — this one and Phase 2 §1.3's `contract_drift` count (M3-P2-OD-2, ruled 2026-07-30, the earlier discovery) — and Phase 8 §7.4 orders the manifest by discovery date, so the earlier claim wins. Corrected here rather than left as a contradiction the manifest would open with |
| **M3-P0-OD-11** ✅ **RULED 2026-07-31** | Licensing. Module 1 (`Codmailling`) carries `LICENSE` + `NOTICE`, resolved explicitly as a Phase-0 deferral ("proprietary / all-rights-reserved"). Module 2 has neither. | Module 1 treated this as Phase 0's job; module 2 skipped it. Module 3 should not inherit the skip by accident. | Adopt module 1's convention at Gate B, or record a deliberate deferral with an owner. Low severity, cheap either way. **RULED: (a) adopt module 1's `LICENSE` + `NOTICE` convention at Gate B.** This doc deliberately offered **no** recommendation — licensing is not an engineering call — so the ruling is the whole decision rather than an endorsement of one. Concretely: `LICENSE` + `NOTICE` land in the Gate B rename commit, carrying module 1's proprietary / all-rights-reserved text with module 3's identity substituted. Two consequences: the files exist **before** Gate B's green is claimed, and module 2's absence of them becomes a shared-code manifest observation (module 3 has them, module 2 does not) rather than a silent asymmetry |

---

## Confirmation

**Nothing in module 2's Phase 0 output was redesigned by this document.** The repo structure,
operating-rules file, real-infrastructure harness, six CI gates, secrets/cert scaffolding,
migration runner and its self-tracking convention, `docker-compose` dev loop, and the nine-item
Definition of Done are inherited as-built and referenced by section
(`Codmailling 2/Docs/history/phase-0-groundwork.md` §1–§9, `definition-of-done.md`). This document
specifies only: which commit to fork, how to fork it, which identifiers change, which gates carry
over versus break later by design, which lines of `CLAUDE.md` change, what the shared-code manifest
records, what Phase 0 must not touch, and how the baseline is proven green.
