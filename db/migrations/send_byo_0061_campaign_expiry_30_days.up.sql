-- send_byo_0061_campaign_expiry_30_days.up.sql -- the OWNER'S DECISION of 2026-11-27: a campaign expires 30 days (2592000 s) after its first job is
-- accepted, not 7 (604800 s, what send_byo_0042 seeded). campaign_expiry_mode is NOT touched: it stays `shadow` (it counts the campaigns it
-- WOULD expire and expires none) until the go-live checklist's promotion rule promotes it.
--
-- WHY THIS IS A DELETE + RE-INSERT, NOT AN UPDATE (CLAUDE.md Rule 1: "default_value is never mutated"). The same precedent as send_byo_0048
-- (unit_price_credits, M3-P7-OD-11) and send_byo_0042's own quarantine_threshold drop: the old row is deleted and a fresh one is seeded at a NEW seed
-- site with its own default, and from here on THIS default_value is the one that is never mutated again. The key keeps its name (Rule 5: a
-- rename would be a second contract break; the key still means "seconds before a campaign expires"), no consumer changes, and the manifest,
-- the consumers ledger and the reference table list the key exactly as before (config_drift and config_consumption compare KEYS, not values).
--
-- WHAT IT DOES NOT MOVE. A campaign already started froze its own expires_at at its first job (send_byo.campaigns.expires_at, platformapi's
-- campaign upsert); only campaigns whose first job arrives after this migration get the new window. A per-user override
-- (send_byo.user_config_overrides.campaign_expiry_sec) still wins over this global. Any live value an operator wrote into the old row is replaced
-- by the new seed (this module is not live yet; go-live is gated by the checklist).
--
-- WHY NO ORDERING INVARIANT IS AFFECTED. The two config-ordering invariants (smtp_operation_timeout_sec < job_max_processing_time_sec, and
-- box_outcome_buffer_ttl_sec >= startup_reconnect_window_sec) name neither this key nor anything it feeds, and the admin PUT validates this key
-- only as an integer (no per-key range case in adminapi/config_validate.go): 2592000 is inside that, and inside the per-user override column's
-- own bound (an INTEGER > 0; 2592000 fits an int4).
DELETE FROM send_byo.config WHERE key = 'campaign_expiry_sec';

INSERT INTO send_byo.config (key, value, default_value, description, value_type) VALUES
('campaign_expiry_sec', '{"v":2592000}', '{"v":2592000}',
 'Seconds after a campaign''s first job is accepted before it expires (default 30 days, 2592000 s: the owner''s decision of 2026-11-27, re-seeded at send_byo_0061; send_byo_0042 first seeded 7 days) — the un-gameable bound on a campaign stuck WAITing on self-recovering accounts forever (a lazy check would never run on exactly the campaign that most needs bounding, since its jobs are never claimed). Resolved override -> global and FROZEN at insert time (a mid-campaign config change never retroactively expires a running campaign) — admin-writable per-user override in send_byo.user_config_overrides.campaign_expiry_sec (NULL = inherit this global). Silent in advance (no warning); notifies + shows in the report on the event. Phase 5 §5.3.',
 'integer');

INSERT INTO send_byo.schema_migrations (version) VALUES ('0061');
