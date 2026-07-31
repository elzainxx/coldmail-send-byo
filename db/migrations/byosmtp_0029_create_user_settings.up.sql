-- byosmtp_0029_create_user_settings.up.sql — Module 3 Phase 1 §4.1, G3. One row per user,
-- user-owned profile-level settings. Row absent = every default applies. Holds routing_strategy,
-- moved here from the pool level per §3.1: poolRoutingStrategy becomes userRoutingStrategy on the
-- same dispatch hot path, same single-row lookup, same accountOrderByForStrategy/ORDER BY set.
--
-- ADR-0008 default-privileges trap: created by the migration admin role then reowned to
-- byosmtp_owner, so byosmtp_module gets ZERO DML until the explicit GRANT below.

CREATE TABLE byosmtp.user_settings (
    user_id          UUID PRIMARY KEY,
    routing_strategy byosmtp.routing_strategy NOT NULL DEFAULT 'round_robin',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT user_settings_user_fk
        FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE
);

ALTER TABLE byosmtp.user_settings OWNER TO byosmtp_owner;

-- Explicit grant (ADR-0008): this table is mutable by design, full DML, no append-only trigger.
GRANT SELECT, INSERT, UPDATE, DELETE ON byosmtp.user_settings TO byosmtp_module;

ALTER TABLE byosmtp.user_settings ENABLE ROW LEVEL SECURITY;
CREATE POLICY user_settings_module_rw ON byosmtp.user_settings
    FOR ALL TO byosmtp_module USING (true) WITH CHECK (true);

INSERT INTO byosmtp.schema_migrations (version) VALUES ('0029');
