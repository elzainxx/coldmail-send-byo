-- byosmtp_0028_drop_pools.down.sql — reverse 0028 (dev/CI only; forward-only in prod).
-- SHAPE-REVERSIBLE, NOT DATA-REVERSIBLE: re-creates byosmtp.pools verbatim from 0004 (incl. its
-- 5 constraints), the pool_kind type from 0003, the 3 pool indexes from 0011, the grant + RLS +
-- policy from 0012, and re-INSERTs the default_shared_pool_id config row from 0010 — but no pool
-- or config-row DATA from before the up migration is recovered (it is gone; this is a fresh
-- empty table and a fresh NULL-valued config row, not a restore).

CREATE TYPE byosmtp.pool_kind AS ENUM (
    'shared',      -- Shared pool, available to all users
    'dedicated'    -- Dedicated pool, owned by a specific user
);
ALTER TYPE byosmtp.pool_kind OWNER TO byosmtp_owner;

CREATE TABLE byosmtp.pools (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL,

    kind byosmtp.pool_kind NOT NULL,
    owner_user_id UUID,

    fallback_pool_id UUID,

    name TEXT NOT NULL,
    routing_strategy byosmtp.routing_strategy NOT NULL DEFAULT 'round_robin',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT pools_user_fk
        FOREIGN KEY (user_id)
        REFERENCES public.users(id)
        ON DELETE RESTRICT,

    CONSTRAINT pools_owner_user_fk
        FOREIGN KEY (owner_user_id)
        REFERENCES public.users(id)
        ON DELETE CASCADE,

    CONSTRAINT pools_fallback_fk
        FOREIGN KEY (fallback_pool_id)
        REFERENCES byosmtp.pools(id)
        ON DELETE SET NULL,

    CONSTRAINT pools_kind_ownership_check
        CHECK (
            (kind = 'shared' AND owner_user_id IS NULL)
            OR (kind = 'dedicated' AND owner_user_id IS NOT NULL)
        ),

    CONSTRAINT pools_fallback_not_self_check
        CHECK (fallback_pool_id IS DISTINCT FROM id)
);

ALTER TABLE byosmtp.pools OWNER TO byosmtp_owner;

CREATE INDEX pools_user_id_kind_idx
    ON byosmtp.pools (user_id, kind);
CREATE INDEX pools_owner_user_id_idx
    ON byosmtp.pools (owner_user_id)
    WHERE owner_user_id IS NOT NULL;
CREATE INDEX pools_fallback_pool_id_idx
    ON byosmtp.pools (fallback_pool_id)
    WHERE fallback_pool_id IS NOT NULL;

GRANT SELECT, INSERT, UPDATE, DELETE ON byosmtp.pools TO byosmtp_module;
ALTER TABLE byosmtp.pools ENABLE ROW LEVEL SECURITY;
CREATE POLICY sender_module_full_access ON byosmtp.pools
    TO byosmtp_module USING (true) WITH CHECK (true);

INSERT INTO byosmtp.config (key, value, default_value, description, value_type) VALUES
('default_shared_pool_id', '{"v": null}', '{"v": null}',
 'Platform-wide default shared pool ID for fallback (NULL until pool created)', 'uuid');

DELETE FROM byosmtp.schema_migrations WHERE version = '0028';
