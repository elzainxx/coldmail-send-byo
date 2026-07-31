-- byosmtp_0028_drop_pools.up.sql — Module 3 Phase 1 §2.1/§2.2/§2.4. Drops byosmtp.pools (no
-- CASCADE needed: 0027 already removed the only inbound FK, accounts_pool_fk), drops the
-- pool_kind enum type (referenced by exactly one column, pools.kind, which goes with the table),
-- and deletes the default_shared_pool_id config row — its consumer (resolveSharedFallback) died
-- at G1; this is the row's own death, completing the M3-P2-OD-1 RESERVED hand-off.
--
-- This is the fork-time-only additive-only exemption in effect (M3-P1-OD-3): removing a table,
-- an enum type, and a config key is normally forbidden by Rule 5, but the exemption covers
-- exactly these five removals (this table, this column [0027], this enum type, this config key,
-- and the five routes [G2's code commit]) and nothing after 0025.

DROP TABLE byosmtp.pools;
DROP TYPE byosmtp.pool_kind;

DELETE FROM byosmtp.config WHERE key = 'default_shared_pool_id';

INSERT INTO byosmtp.schema_migrations (version) VALUES ('0028');
