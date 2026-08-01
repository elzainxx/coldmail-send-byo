-- byosmtp_0039_from_gate_config_seeds.down.sql — remove the seven seeded rows.

DELETE FROM byosmtp.config WHERE key IN (
    'from_gate_mode',
    'from_dns_resolver',
    'from_dns_lookup_timeout_ms',
    'from_dns_total_budget_ms',
    'from_dns_cache_ttl_sec',
    'from_dns_negative_cache_ttl_sec',
    'from_spf_lookup_limit_posture'
);

DELETE FROM byosmtp.schema_migrations WHERE version = '0039';
