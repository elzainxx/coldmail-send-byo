-- send_byo_0052_dispatch_mode_ladder.up.sql — cross-module alignment fix-plan item 12
-- (classification 13.1, module-comparison-report §7.4, canonical-module-contract.md §1.11.1).
--
-- WHAT THIS ADDS. One string-enum config key, `dispatch_mode`, seeded at `serving`:
--
--   serving | accept_and_defer | refuse_new | stopped
--
-- It is the graduated form of a control that was binary. `dispatch_enabled` halted every tenant's
-- claim path or none, so a partial incident (one provider degrading, a suspected bad config, one
-- user's accounts misbehaving) had exactly one available response: stop everything.
--
-- SEEDED WITH ITS CONSUMER, IN THIS SAME CHANGE — ADR-0016's discipline, stated there as "seed at
-- real consumption, never before," and the rule send_byo_0051 was written to enforce. The consumer is
-- internal/dispatch/rung.go (resolveRung + the three predicates), read at three gate sites
-- (loop.go's Tick, dispatch.go's TryDispatch, reclaim.go's ReclaimTick) and one intake site
-- (platformapi handlers.go's handleRun). Docs/config-consumers.txt records it with a grep pattern, so
-- the BLOCKING config_consumption gate proves the consumer exists on every CI run rather than taking
-- this header's word for it. There is no RESERVED marker here and none is available: the tree is at
-- its last phase, which is exactly the state that rule was built to make honest.
--
-- WHY `dispatch_enabled` IS NOT DROPPED, though this key supersedes its role.
--
-- send_byo_0051 dropped a config row and set out the test it must pass — ADR-0016's two conditions.
-- Applied here, literally, condition (1) FAILS and the key therefore stays:
--
--   (1) "The key never had a genuine module-3 consumer at ANY point." False, several times over:
--       dispatch/loop.go:92 and dispatch/dispatch.go:239 both gate on it; adminapi/health_store.go
--       derives the §3.2 health snapshot's `dispatch_paused` from it; platformapi/campaign_report.go
--       reads it live for the campaign report's `dispatch_paused`; and Docs/disaster-recovery.md
--       instructs an operator to set it in three separate procedures (the post-restore re-send hold,
--       the send freeze, and the stale-credential hold). This is a live control with live consumers,
--       not dead inheritance.
--
--   ADR-0016 addresses this case by name and declines it: a future phase citing "nobody uses this
--   anymore" for a key that DID have a real module-3 consumer "is still NOT covered and still needs
--   its own ruling." This migration does not take that ruling. Nothing is dropped, renamed, or
--   reinterpreted; `dispatch_enabled` keeps its name, its default, its type and its exact meaning.
--
-- SO HOW DO TWO DIALS GOVERN ONE CONTROL WITHOUT HALF-PULLING IT? They are not independent. The
-- effective rung is the MORE SEVERE of the two (rung.go's resolveRung), with `dispatch_enabled=false`
-- acting as a SEVERITY FLOOR of accept_and_defer.
--
-- That floor is not a compatibility shim — it is a restatement of what the boolean already did.
-- Verified against the tree, `dispatch_enabled=false` gates only the two CLAIM sites: intake keeps
-- accepting, reclaim/outcome/heartbeat/notifications keep running (its own throttled log line says
-- so in those words). That is accept-and-defer precisely. So every existing runbook, DR step and test
-- that sets `dispatch_enabled=false` remains true to the letter, and no pair of values can leave a
-- pulled rung half-effective, because a maximum has no such state.
--
-- The alternative shape — two independent levers, one for intake and one for the drainer — is the
-- specific thing M1's ADR-0015 §1 identifies as "exactly how one half stays running." Avoided by
-- construction here, and asserted over the full 4x2 matrix by
-- TestDispatchRung_EnabledFalseIsAFloorNotASecondLever.
--
-- WHY value_type='string' AND NOT A pg ENUM. Same reasoning autoquarantine_mode (send_byo_0021)
-- already settled for this table: send_byo.config is a generic key/value table whose value_type
-- column is the discriminator, the admin PUT validator enforces the accepted set at write time
-- (config_validate.go, alongside autoquarantine_mode's), and the Control-side loader fails safe on an
-- unrecognized value regardless. Minting a pg enum for one config row would put a value set that may
-- gain a rung behind a DDL migration, and Rule 5's additive-only discipline is easier to honour in
-- the validator than in a type.
--
-- NO SCHEMA CHANGE. One INSERT into an existing table. No column, no index, no constraint, no grant
-- change, and nothing in send_byo.jobs or job_events is touched — the ladder changes what the Control
-- DOES, never what is recorded (Rule 5: outcomes stay permanent by construction).

INSERT INTO send_byo.config (key, value, default_value, description, value_type) VALUES
('dispatch_mode', '{"v":"serving"}', '{"v":"serving"}',
 'Graduated dispatch kill-switch ladder (fix-plan item 12, contract §1.11.1). One of
serving | accept_and_defer | refuse_new | stopped, in increasing severity. serving = normal
operation. accept_and_defer = POST /v1/run still accepts and queues, but nothing is claimed or
pushed; reclaim, outcome ingestion and heartbeats continue. refuse_new = as accept_and_defer, and
POST /v1/run is refused 503 paused so no backlog accumulates; in-flight work still drains and is
still reclaimed. stopped = as refuse_new, and the reclaim sweep is frozen too, so an expired lease is
left in place rather than requeued (nothing is stranded: reclaim''s predicate is a DB read, so the
lease is still expired on the first tick after release). Outcome ingestion is NEVER gated at any rung
— an outcome is the record of a mail that has already left, and discarding it would destroy evidence
rather than prevent a send. Composes with dispatch_enabled as a MAXIMUM, never as a second
independent lever: dispatch_enabled=false is a severity floor of accept_and_defer, which is exactly
what that boolean already did. An unrecognized value falls back to accept_and_defer (level=error) —
no mail leaves under an unreadable policy, and one typo does not freeze the reclaim sweep.', 'string');

INSERT INTO send_byo.schema_migrations (version) VALUES ('0052');
