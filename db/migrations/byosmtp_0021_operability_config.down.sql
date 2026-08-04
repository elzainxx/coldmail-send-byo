-- sender_0021_operability_config.down.sql — reverse of the Phase 9.5 config seed. Deletes exactly
-- the eleven keys sender_0021 inserted and un-records the version. Additive-only + reversible (DoD).
--
-- variation_zero_width_rate_pct is DROPPED later in the chain, at byosmtp_0049 (M3-P8-OD-8), and
-- unit_price_credits is delete+re-seeded at 0048 (M3-P7-OD-11) — this list is deliberately left
-- intact for both. A full down-chain reaches 0021 only after 0049's and 0048's own downs have already
-- restored those rows verbatim, so this DELETE removes what is genuinely there; and a DELETE of an
-- absent row is a no-op, so an out-of-order or partial down cannot fail on it either.
DELETE FROM byosmtp.config WHERE key IN
 ('dispatch_enabled','autoquarantine_mode','autopause_enabled','variation_enabled',
  'alert_webhook_url','health_metrics_window_sec','brain_singleton_acquire_timeout_sec',
  'unit_price_credits','variation_comment_intensity_rates','variation_zero_width_rate_pct',
  'variation_css_micro_params');
DELETE FROM byosmtp.schema_migrations WHERE version = '0021';
