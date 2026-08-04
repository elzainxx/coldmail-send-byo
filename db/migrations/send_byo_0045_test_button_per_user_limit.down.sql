-- send_byo_0045_test_button_per_user_limit.down.sql — remove the seeded row.

DELETE FROM send_byo.config WHERE key = 'test_button_rate_limit_per_user_per_day';

DELETE FROM send_byo.schema_migrations WHERE version = '0045';
