-- send_byo_0036_account_config_seeds.down.sql — remove the six seeded rows.

DELETE FROM send_byo.config WHERE key IN (
    'account_test_rate_limit_per_day',
    'account_count_cap_per_user',
    'account_retention_window_sec',
    'bulk_add_verify_interval_ms',
    'account_purge_sweep_interval_ms',
    'account_purge_delete_batch_size'
);

DELETE FROM send_byo.schema_migrations WHERE version = '0036';
