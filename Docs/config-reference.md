# Config reference (living mirror of Phase 9 §4)

Authoritative source: **Phase 9 §4** (`../Docs/phase-9-polish-and-documentation.md`) — the
50-key consolidated reference. This file is the checked-in mirror, verified by the CI
`config_drift` gate against the live `sender.config` seeds.

## Phase 0 state

**Phase 0 seeds ZERO config keys.** `sender.config` is created in Phase 1 (`sender_0010`);
the keys arrive across Phases 1–9.5. The drift gate therefore diffs against a **phase-aware
manifest** (`config-manifest.txt`), which is empty at Phase 0 — the gate asserts that the
live `sender.config` key set equals the manifest (both empty until Phase 1). This is what
makes "no hardcoded tuning constant" checkable from the very first phase that seeds a key.

| key | default_value | value_type | seeding migration | description |
|-----|---------------|-----------|-------------------|-------------|
| _(none at Phase 0)_ | | | | |

## Both config-ordering invariants (enforced at config-load, Phase 7 §5.3 authoritative)

- `smtp_operation_timeout_sec < job_max_processing_time_sec`
- `box_outcome_buffer_ttl_sec >= startup_reconnect_window_sec`

A config set violating either is rejected with external code `config_ordering_violation`.
These keys are seeded in later phases; the gate asserts the invariants from live values
once the keys exist.
