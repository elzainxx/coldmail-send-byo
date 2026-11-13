# ADR-0016: dropping `quarantine_threshold` narrowly extends M3-P1-OD-3's fork-time-only exemption

- **Status:** accepted
- **Date:** 2026-07-31
- **Supersedes:** —
- **Superseded-by:** —

## Context

Phase 5 replaces module 2's score-as-quarantine-trigger design with dual quarantine triggers
(`auth_failure` immediate, consecutive-failure streak, failure-rate window — §4). The inherited
config key `quarantine_threshold` (seeded `sender_0010:31`, `default_value 30`, "account_status_score
below which account is auto-quarantined") loses its only consumer the moment §4 lands: nothing in
module 3 reads it, ever, at any point past this phase.

This collides with three locked rules at once:

1. **CLAUDE.md Rule 5** (additive-only, contracts never removed) forbids dropping a seeded config
   key outright.
2. **CLAUDE.md Rule 1** (config-not-code) forbids a seeded key with no real consumer passing
   `config_consumption.sh` by any means other than an honestly-expiring `RESERVED` marker.
3. **M3-P1-OD-3**'s own ruling states the additive-only exemption module 3 used to drop `pool_kind`
   and four other module-2-era objects at the Phase 1 baseline is **fork-time only** and **expires
   at Phase 1's close** — Phase 5 is well past that close, so invoking the same exemption again
   naively would be re-opening a door M3-P1-OD-3 explicitly shut.

Four options were on the table and each was rejected for a stated reason: (a) drop it — blocked by
the expired exemption and Rule 5 read literally; (b) repurpose it as a display-warning threshold —
Rule 5 forbids repurposing outright, and it is a real semantic change ("quarantine at" becoming
"warn at") dressed as a rename; (c) mark it `RESERVED` — Phase 2's own `RESERVED`-expiry invariant
(ADR-0014) requires a named future consumer, and there is no future phase that will ever consume
`quarantine_threshold` specifically, so the marker could never expire honestly; (d) leave a
vestigial consumer branch reading it and doing nothing — Rule 1 calls a literal governing no real
behavior a bug by definition.

The ruling: **none of the four — split it.** Drop `quarantine_threshold` entirely; add a
differently-named key, `score_alert_threshold`, with a genuine future consumer (Phase 7's warning
subsystem, §3.1). The rationale for why dropping is permissible here, despite M3-P1-OD-3's
exemption having formally expired: that exemption's own reasoning was "additive-only protects
consumers from breaking changes; module 3 has none for objects that never had a module-3 consumer."
`quarantine_threshold` never had a module-3 consumer at any point in module 3's own history — Phase
1 through Phase 4 all inherited the seed row unread. Deleting it removes dead inheritance, not a
live contract module 3 itself ever honored. This is not the same fact pattern M3-P1-OD-3 closed
(that ruling covered objects removed AT the fork baseline, before Phase 1 shipped); this is a new,
narrower fact pattern the ruling's own text requires an ADR to state explicitly, precisely so this
extension is not read as a general re-opening of Rule 5 for any future "nobody's using this" claim.

## Decision

**`quarantine_threshold` is dropped (migration `0042`'s `DELETE FROM byosmtp.config`, with the
matching down-file re-`INSERT` at its original `default_value` and description, verbatim, per
Rule 1's "`default_value` is never mutated").** Its replacement, `score_alert_threshold`, is a
genuinely new key — not a rename, not a repurpose — seeded later, in Phase 7, at the point it
gains a real consumer (§3.1's warning subsystem), never earlier, so it is never itself a Rule-1
violation in the gap between being seeded and being consumed.

**This extension of M3-P1-OD-3's exemption is narrow and does not reopen the exemption
generally.** It applies ONLY when both of the following hold simultaneously: (1) the key never had
a genuine module-3 consumer at any point in module-3's own build history (not merely "no consumer
right now" — a key can lose a consumer mid-build and that alone does not qualify), and (2) the
replacement, if any, is a new key under a new name with its own seed-at-real-consumption discipline,
never the same key reinterpreted. A future phase citing "nobody uses this anymore" as grounds to
drop a key that DID have a real module-3 consumer at some point is NOT covered by this ADR and
would need its own ruling.

## Consequences

- Migration `0042` drops one key and adds ten (Phase 5's own nine dual-quarantine dials plus this
  net effect) — the repo-wide inherited count falls to 52; Phase 7 later re-adds one
  (`score_alert_threshold`), for a total both `config_drift` and `config_consumption` check
  unchanged at 85 (`Docs/history/phase-5-autonomous-behaviour-and-limits.md` §10.1's own accounting).
- `config-consumers.txt`'s `quarantine_threshold` line is deleted in the same migration that drops
  the row — ledger and schema move together, never one ahead of the other.
- `score_alert_threshold` is deliberately NOT seeded in Phase 5, even though this ADR is written
  here — seeding it before Phase 7 gives it a consumer would recreate the exact Rule-1 defect this
  whole decision exists to avoid.
- **This ADR was written 2026-08-02, two days after the ruling and the migration both landed
  (2026-07-31).** Found missing by Phase 6's mechanical landing check (the ruling's own text
  required this ADR as part of landing the drop, and the drop landed while the ADR did not).
  Recorded here as a backfill of a decision that was already correctly implemented — `0042`'s drop
  and its down-file's exact-`default_value` restore were both already in the tree and already
  verified by this same landing check before this ADR was written.

## Links

- CLAUDE.md Rule 1 (config-not-code, no unconsumed dial), Rule 5 (additive-only, never repurpose).
- `Docs/open-decisions.md` — M3-P1-OD-3 (the fork-time-only exemption this ADR narrowly extends)
  and M3-P5-OD-1 (this decision's own ruling text).
- `Docs/history/phase-5-autonomous-behaviour-and-limits.md` §4, §10.1, §10.3 (the drop's implementation and
  accounting).
- `Docs/adr/ADR-0014-reserved-config-marker-requires-a-mechanical-expiry.md` (ADR-0014 —
  why option (c), `RESERVED`, was not available here).
- `db/migrations/byosmtp_0042_seed_phase5_config.{up,down}.sql`.
