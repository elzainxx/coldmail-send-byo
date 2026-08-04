-- send_byo_0048_unit_price_zero.down.sql — delete this migration's re-seeded unit_price_credits
-- row, restore the ORIGINAL send_byo_0021 row VERBATIM (Rule 1: default_value is never mutated, so
-- the down file restores it byte-for-byte rather than approximating).
DELETE FROM send_byo.config WHERE key = 'unit_price_credits';

INSERT INTO send_byo.config (key, value, default_value, description, value_type) VALUES
('unit_price_credits', '{"v":1}', '{"v":1}',
 'Credits charged per recipient (quote/describe). Was a documented constant; default unchanged, now retunable without a redeploy. Phase 9.5 S10 §2.4.',
 'integer');

DELETE FROM send_byo.schema_migrations WHERE version = '0048';
