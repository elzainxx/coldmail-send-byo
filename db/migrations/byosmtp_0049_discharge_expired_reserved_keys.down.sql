-- byosmtp_0049_discharge_expired_reserved_keys.down.sql — restore both dropped keys VERBATIM from
-- their ORIGINAL seed sites (Rule 1: default_value is never mutated, so the down file restores
-- byte-for-byte what existed before this migration rather than approximating it).
--
--   retry_count_4xx                : byosmtp_0010_create_config_and_seed.up.sql:32
--   variation_zero_width_rate_pct  : byosmtp_0021_operability_config.up.sql:86-88
--
-- score_alert_threshold is untouched by the up file, so it is untouched here.

INSERT INTO byosmtp.config (key, value, default_value, description, value_type) VALUES
('retry_count_4xx', '{"v": 3}', '{"v": 3}', 'Number of retries for 4xx transport errors before requeue', 'integer'),
('variation_zero_width_rate_pct', '{"v":5}', '{"v":5}',
 'Percent of word boundaries eligible for zero-width insertion (variation_zero_width). Was hardcoded ~5; default unchanged. Box-consumed via push_job.variation. Phase 9.5 S11 §2.4.',
 'integer');

DELETE FROM byosmtp.schema_migrations WHERE version = '0049';
