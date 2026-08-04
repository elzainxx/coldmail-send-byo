-- send_byo_0029_create_user_settings.up.sql — Module 3 Phase 1 §4.1, G3. One row per user,
-- user-owned profile-level settings. Row absent = every default applies. Holds routing_strategy,
-- moved here from the pool level per §3.1: poolRoutingStrategy becomes userRoutingStrategy on the
-- same dispatch hot path, same single-row lookup, same accountOrderByForStrategy/ORDER BY set.
--
-- ADR-0008 default-privileges trap: created by the migration admin role then reowned to
-- send_byo_owner, so send_byo_app gets ZERO DML until the explicit GRANT below.

CREATE TABLE send_byo.user_settings (
    user_id          UUID PRIMARY KEY,
    routing_strategy send_byo.routing_strategy NOT NULL DEFAULT 'round_robin',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT user_settings_user_fk
        FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE
);

ALTER TABLE send_byo.user_settings OWNER TO send_byo_owner;

-- Explicit grant (ADR-0008): this table is mutable by design, full DML, no append-only trigger.
GRANT SELECT, INSERT, UPDATE, DELETE ON send_byo.user_settings TO send_byo_app;

ALTER TABLE send_byo.user_settings ENABLE ROW LEVEL SECURITY;
CREATE POLICY user_settings_module_rw ON send_byo.user_settings
    FOR ALL TO send_byo_app USING (true) WITH CHECK (true);

INSERT INTO send_byo.schema_migrations (version) VALUES ('0029');
