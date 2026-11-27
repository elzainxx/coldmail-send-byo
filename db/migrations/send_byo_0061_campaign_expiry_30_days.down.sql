-- send_byo_0061_campaign_expiry_30_days.down.sql -- reverse send_byo_0061: delete the re-seeded campaign_expiry_sec row and restore the ORIGINAL
-- send_byo_0042 row VERBATIM (7 days, {"v":604800}). Rule 1: default_value is never mutated, so the down file restores the old row byte for byte
-- rather than approximating it, exactly as send_byo_0048's down does for unit_price_credits.
--
-- WHAT THIS DOWN LOSES. A live value an operator wrote after 0061 (the row is replaced, so it is back at the 7-day seed). Campaigns already
-- started keep the expires_at they froze at their first job (send_byo.campaigns.expires_at), so nothing in flight moves.
DELETE FROM send_byo.config WHERE key = 'campaign_expiry_sec';

INSERT INTO send_byo.config (key, value, default_value, description, value_type) VALUES
('campaign_expiry_sec', '{"v":604800}', '{"v":604800}',
 'Seconds after a campaign''s first job is accepted before it expires (default 7 days) — the un-gameable bound on a campaign stuck WAITing on self-recovering accounts forever (a lazy check would never run on exactly the campaign that most needs bounding, since its jobs are never claimed). Resolved override -> global and FROZEN at insert time (a mid-campaign config change never retroactively expires a running campaign) — admin-writable per-user override in send_byo.user_config_overrides.campaign_expiry_sec (NULL = inherit this global). Silent in advance (no warning); notifies + shows in the report on the event. Phase 5 §5.3.',
 'integer');

DELETE FROM send_byo.schema_migrations WHERE version = '0061';
