-- send_byo_0053_shadow_mode_for_every_autonomous_mutator.up.sql — cross-module alignment fix-plan
-- item 13 (classification 13.3, canonical-module-contract.md §1.11.3).
--
-- WHAT THIS ADDS. FOUR string-enum config keys, each seeded at `shadow`:
--
--   autopause_mode          off | shadow | enforce   (seeded shadow)
--   retention_mode          off | shadow | enforce   (seeded shadow)
--   campaign_expiry_mode    off | shadow | enforce   (seeded shadow)
--   account_purge_mode      off | shadow | enforce   (seeded shadow)
--
-- FOUR, NOT TWO. Module 2's equivalent migration (send_managed_0026) adds two, because module 2 has two
-- undialled autonomous mutators. THIS module has four: it also runs the campaign-expiry sweep (Phase 5
-- §5.3) and the account purge sweep (Phase 3 §6.3), neither of which exists in module 2 and neither of
-- which had a mode dial. Porting module 2's key list verbatim would have shipped the two most
-- irreversible writers in this module still ungated — which is exactly the shape of defect item 13
-- exists to remove, arriving in the fix for it. The dial count is a property of THIS module's writers,
-- not of the module it is being aligned with.
--
-- THE FULL AUDIT, INCLUDING WHAT IS DELIBERATELY *NOT* DIALLED. Recorded because "we checked
-- everything" is worthless without the list, and because the exclusions are the part a later reader
-- should challenge:
--
--   GAINS A DIAL HERE
--   * auto-pause (dispatch/account_effect.go) — sets accounts.paused/pause_reason='throttle' on a
--     throttle outcome. Autonomous, previously all-or-nothing via `autopause_enabled`.
--   * the send_logs retention sweep (dispatch/retention.go) — DELETEs expired send_byo.send_logs rows.
--     Irreversible: recipient, subject, body and SMTP response are gone, and flipping the dial back
--     does not bring them back. It had no mode dial at all, only an interval and a batch size.
--   * the campaign expiry sweep (dispatch/campaign_expiry.go) — marks a campaign expired and fails its
--     ready queued jobs to TERMINAL `failed`. Terminal is terminal: a job failed by a mis-set
--     `campaign_expiry_sec` is not re-queued by fixing the config afterwards. This is the writer whose
--     threshold an operator is least able to calibrate in advance, since it fires on the ABSENCE of
--     activity, and the ladder is what lets them watch it for a week before it can act.
--   * the account purge sweep (dispatch/purge.go) — hard-DELETEs a soft-deleted account, credential
--     ciphertext and all. The single most irreversible write in either module, and the one with the
--     least tolerance for a mis-seeded window: `account_retention_window_sec` set one unit off purges
--     accounts a user could still have restored. There is no honest exclusion reason for a writer that
--     destroys a credential on its own initiative.
--
--   ALREADY DIALLED — no change
--   * auto-quarantine (dispatch/account_effect.go) — `autoquarantine_mode`, already off/shadow/enforce
--     seeded shadow. The pattern the four new dials copy.
--   * claim / lease / push / reclaim / rescan / exhaustion (dispatch/*.go, workerlink/stream.go) —
--     governed by `dispatch_mode`'s four-rung ladder (send_byo_0052, fix-plan item 12) plus
--     `dispatch_enabled`. A rung already stops each of them. A kill-switch is not a mutator, which is
--     why they are on a ladder ordered by SEVERITY rather than by ACTIVITY.
--
--   DELIBERATELY NOT DIALLED, with the reasoning
--   * score recompute (dispatch/account_score.go) — writes accounts.score/consecutive_failures, but a
--     score is DERIVED STATE, not an action: it is the observation the quarantine and pause decisions
--     then read. Shadowing it would store a stale number and make the dialled mutators act on a lie,
--     which previews nothing.
--   * auto-unpause (dispatch/eligibility.go) — the REVERSAL half of auto-pause. Shadowing a reversal is
--     actively harmful: it leaves real pauses in place forever, manufacturing the stuck-account incident
--     the observation period exists to avoid. A dial gates an ACTION, never its REVERSAL.
--   * outcome ingestion (dispatch/outcome.go) — records what a worker REPORTED. Never autonomous; the
--     Control is transcribing an external event. Item 12 established that outcome ingestion is not gated
--     at any rung, because refusing it destroys evidence rather than preventing an action.
--   * credential verification (dispatch/verify.go) — writes credential_verification_state for an
--     account the USER just asked to be verified. Operator/user-initiated, not autonomous.
--   * test-send (dispatch/send_test_action.go) — a job the user explicitly requested. Not autonomous.
--   * notification delivery (dispatch/notify.go) — sends the message and marks the row delivered. It is
--     the OBSERVATION CHANNEL itself: shadowing it would suppress the very reports a shadow mode exists
--     to produce, so an operator running an observation period would see nothing and conclude nothing
--     was happening. `notifications_enabled` already exists for the case where someone genuinely wants
--     the channel off.
--   * every write in internal/adminapi and internal/platformapi — caller- or operator-initiated. Nobody
--     needs a mode dial to preview their own request.
--
-- THAT LIST IS NOT LOAD-BEARING PROSE. A comment claiming an audit was complete is exactly the
-- "warning instead of a mechanism" defect this alignment pass keeps finding, so the audit is also
-- enforced by a TEST:
--   dispatch.TestMutatorMode_EveryAutonomousMutatorIsDialledOrExplicitlyExcluded
-- (services/control/internal/dispatch/mutator_mode_test.go). It discovers every FILE in
-- internal/dispatch that writes to send_byo.* and requires each to be either enumerated as dialled,
-- with its dial actually REFERENCED in that file, or enumerated as excluded with a recorded reason.
-- Both halves are checked, so a site LOSING its gate fails there too rather than passing silently, and
-- both maps are checked for stale entries. A new autonomous mutator cannot ship enforcing on day one
-- without that test going red naming its file.
--
-- SCOPE OF THAT MECHANISM, STATED HONESTLY: it scans internal/dispatch only, NOT internal/workerlink.
-- workerlink's writes are lease/push bookkeeping already governed by dispatch_mode's rung ladder
-- (send_byo_0052) and are listed under ALREADY DIALLED above — but that half rests on the audit here
-- rather than on the discovery test, and saying otherwise would be the same defect this paragraph
-- exists to avoid.
--
-- SEEDED `shadow`, WHICH CHANGES BEHAVIOUR ON UPGRADE, DELIBERATELY AND WITH A FLAG.
--
-- A migrated deployment stops auto-pausing, stops deleting expired send_logs, stops expiring campaigns
-- and stops purging soft-deleted accounts until an operator promotes each dial. The alternative — seed
-- `enforce` to preserve behaviour and let each deployment opt into observation — was rejected: a safety
-- default that must be opted into is not a default, and it would make this item cosmetic for exactly
-- the deployments where the risk lives.
--
-- TWO of the four carry a STANDING COST while unpromoted, and both are REQUIRED go-live steps rather
-- than niceties (Docs/go-live-checklist.md carries them as gates, Docs/operator-runbook.md as procedure):
--   * retention_mode=shadow — send_byo.send_logs (recipient, subject, body, headers, SMTP response)
--     GROWS WITHOUT BOUND, and the retention promise in Docs/security-posture.md is not being kept
--     while it does.
--   * account_purge_mode=shadow — a soft-deleted account's credential_ciphertext OUTLIVES its promised
--     retention window. That is a privacy commitment being missed, not merely disk: the user asked for
--     the account to be gone.
-- The other two (autopause_mode, campaign_expiry_mode) cost visibility rather than compliance while
-- unpromoted, and are genuinely deferrable to a post-launch observation window.
--
-- Every shadow mode LOGS what it would have done on every tick, so the cost of leaving a dial
-- unpromoted is continuously visible rather than silent. A shadow mode that observes silently is
-- indistinguishable from an off switch, and an operator would read that silence as "nothing is
-- happening" and promote a dial they had never actually watched.
--
-- HOW autopause_mode COMPOSES WITH THE BOOLEAN IT JOINS — A CEILING, NOT A FLOOR.
--
-- `autopause_enabled` is RETAINED (Rule 5, and it has live consumers: account_effect.go's gate, the
-- health snapshot, the operator docs). It composes as a CEILING: `autopause_enabled=false` CAPS the
-- effective mode at `off`.
--
-- THIS IS THE OPPOSITE DIRECTION FROM send_byo_0052's FLOOR, AND THAT IS NOT AN INCONSISTENCY. For a
-- KILL-SWITCH (dispatch_mode) `false` means "do LESS sending", so the boolean must be able to make the
-- effective state MORE severe: a floor. For a MUTATOR ENABLE (autopause_enabled) `false` means "take
-- FEWER actions", so it must be able to make the state LESS active: a ceiling. Stated properly the rule
-- is identical — THE BOOLEAN CAN ONLY EVER REDUCE WHAT THE SYSTEM DOES, NEVER INCREASE IT. A floor on a
-- severity scale and a ceiling on an activity scale are the same sentence. Getting it backwards is the
-- bug worth guarding: a `false` boolean that let `enforce` through would mean an operator who disabled
-- auto-pause still got auto-pauses.
--
-- The other three dials join no boolean. `retention_sweep_interval_ms`/`retention_delete_batch_size`,
-- `campaign_expiry_sweep_interval_ms`/`campaign_expiry_sec` and `account_purge_sweep_interval_ms`/
-- `account_purge_delete_batch_size`/`account_retention_window_sec` are RATE and THRESHOLD controls, not
-- enables, and are all untouched here. Folding rate into mode would silently change sweep throughput on
-- upgrade for anyone who had tuned them, on top of the mode change the upgrade already makes.
--
-- FAIL-SAFE ON AN UNRECOGNIZED VALUE: all four resolve to `shadow`, never `enforce`, never `off`.
-- `enforce` would let a typo silently gain the power to act — and for account_purge_mode, the power to
-- destroy a credential. `off` would silently disable a control an operator believes is running, which is
-- M1's D2 finding (a control that looks normal while being ignored). `shadow` is the only value that
-- both refuses to act AND keeps reporting. Logged at level=error, and rejected at the admin PUT boundary
-- so a typo is reported rather than quietly demoting a control the operator thought they promoted. This
-- matches autoquarantine_mode's existing fallback exactly.
--
-- WHY value_type='string' AND NOT A pg ENUM: identical reasoning to autoquarantine_mode and
-- dispatch_mode. send_byo.config is a generic key/value table with value_type as the discriminator; the
-- admin PUT validator enforces the accepted set at write time; the loader fails safe regardless.
--
-- NO SCHEMA CHANGE. Four INSERTs into an existing table. No column, no index, no constraint, no grant
-- change. Nothing in send_byo.jobs or job_events is touched: these dials change what the Control DOES,
-- never what is recorded.

INSERT INTO send_byo.config (key, value, default_value, description, value_type) VALUES
('autopause_mode', '{"v":"shadow"}', '{"v":"shadow"}',
 'Shadow ladder for auto-pause, the throttle-response mutator (fix-plan item 13, contract §1.11.3).
One of off | shadow | enforce. enforce = the pre-0053 behaviour: a throttle outcome sets accounts.paused
with pause_reason=''throttle''. shadow (DEFAULT, launch-safety, adopted from module 1''s recommend_only)
= the decision is made and recorded as a would-pause account_events observation row, but the account is
NOT paused and keeps sending. off = the decision is not evaluated at all and no row is written. Composes
with the RETAINED autopause_enabled boolean as a CEILING: false caps the effective mode at off, so an
operator who disabled auto-pause never gets an auto-pause. (Note the direction: dispatch_mode composes
with its boolean as a severity FLOOR, this one as an activity CEILING — both are the same rule, that the
boolean can only ever reduce what the system does.) An unrecognized value falls back to shadow
(level=error), never to enforce and never to off. Manual pause/unpause is unaffected at every mode, and
auto-unpause is NOT gated by this dial — it is the reversal half, and blocking a reversal would leave
real pauses in place forever.', 'string'),

('retention_mode', '{"v":"shadow"}', '{"v":"shadow"}',
 'Shadow ladder for the send_logs retention sweep (fix-plan item 13, contract §1.11.3). One of
off | shadow | enforce. enforce = the pre-0053 behaviour: expired send_byo.send_logs rows are DELETEd in
bounded batches. shadow (DEFAULT, launch-safety) = the sweep COUNTS the rows it would have deleted and
logs that count, deleting nothing. off = no sweep and no count. OPERATIONAL WARNING, and the reason this
key is on the go-live checklist: at the seeded default NOTHING IS EVER DELETED, so send_byo.send_logs
grows without bound and the retention promise in Docs/security-posture.md is not being kept until an
operator promotes this to enforce. That promotion is a REQUIRED go-live step, not optional. This dial
governs ONLY send_logs (debug content); jobs and job_events are never touched by the sweep at any mode,
so no outcome record is ever at risk. An unrecognized value falls back to shadow (level=error) — a typo
must not gain the power to delete, and must not silently disable a sweep an operator believes is running.
retention_sweep_interval_ms and retention_delete_batch_size are rate controls and are unaffected.',
 'string'),

('campaign_expiry_mode', '{"v":"shadow"}', '{"v":"shadow"}',
 'Shadow ladder for the campaign expiry sweep (fix-plan item 13, contract §1.11.3; the sweep itself is
Phase 5 §5.3). One of off | shadow | enforce. enforce = the pre-0053 behaviour: a campaign past
campaign_expiry_sec with non-terminal jobs is marked expired_at, its READY queued jobs are failed to
TERMINAL state failed, and one notification is enqueued. shadow (DEFAULT, launch-safety) = the sweep
identifies the campaigns it WOULD expire and logs them with the count of jobs that would be failed,
changing nothing. off = the sweep does not run at all. Why this writer needed a ladder: the job failures
are TERMINAL, so a mis-set campaign_expiry_sec is not undone by correcting the config afterwards, and the
sweep fires on the ABSENCE of activity — which is the hardest condition for an operator to predict in
advance and therefore the one most worth watching for a week before it can act. An unrecognized value
falls back to shadow (level=error). Suspension is unaffected at every mode: a campaign with
clock_paused_at set is excluded by the partial index the sweep reads, not by this dial.
campaign_expiry_sec and campaign_expiry_sweep_interval_ms are threshold and rate controls and are
unaffected.', 'string'),

('account_purge_mode', '{"v":"shadow"}', '{"v":"shadow"}',
 'Shadow ladder for the account purge sweep (fix-plan item 13, contract §1.11.3; the sweep itself is
Phase 3 §6.3). One of off | shadow | enforce. enforce = the pre-0053 behaviour: a soft-deleted account
whose purge_after has elapsed is hard-DELETEd, credential_ciphertext/nonce/key_id and all. shadow
(DEFAULT, launch-safety) = the sweep COUNTS the accounts it would purge and logs that count, deleting
nothing. off = no sweep and no count. This is the MOST IRREVERSIBLE write in the module: a purged
credential cannot be recovered by any means, and account_retention_window_sec set one unit off purges
accounts a user could still have restored — which is why it gets an observation period rather than an
exclusion. OPERATIONAL WARNING, and the reason this key is on the go-live checklist: at the seeded
default a soft-deleted account''s credential OUTLIVES its promised retention window, which is a privacy
commitment being missed and not merely disk usage. Promoting this to enforce is a REQUIRED go-live step.
An unrecognized value falls back to shadow (level=error) — a typo must never gain the power to destroy a
credential. account_retention_window_sec, account_purge_sweep_interval_ms and
account_purge_delete_batch_size are threshold and rate controls and are unaffected.', 'string');

INSERT INTO send_byo.schema_migrations (version) VALUES ('0053');
