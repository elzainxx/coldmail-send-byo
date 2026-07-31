-- byosmtp_0030_create_user_config_overrides.up.sql — Module 3 Phase 1 §4.2, G3. Admin-writable
-- only, one table, several dials. Nullable columns; NULL = fall through to the global
-- byosmtp.config key. Separate from user_settings because the WRITE AUTHORITY differs (Phase 6's
-- exit criterion: "Users cannot reach pacing or concurrency through any route" — a structural
-- property of two tables, not a per-column condition inside one generic UPDATE). M3-P1-OD-5 ruled
-- keep three tables: Phase 6 §4.2's privilege boundary rests on their being separately reached.
--
-- No config key is seeded here. The global counterparts for max_campaign_size/max_queued_jobs/
-- campaign_expiry_sec are Phase 5's to seed, where they get a real consumer — seeding them now
-- would be a config_consumption violation (a seeded, documented, drift-clean key nothing reads).
--
-- M3-P1-OD-8 (flagged, not fixed by schema): a partial override (e.g. only send_delay_min_sec set)
-- can invert the pacing-ordering invariant against the GLOBAL counterpart the row-level CHECK
-- cannot see. Enforcement on the RESOLVED set, inside the write's own transaction under the
-- config-write serializer (advisory lock 8_300_000_001), is Phase 6's to implement.

CREATE TABLE byosmtp.user_config_overrides (
    user_id                 UUID PRIMARY KEY,
    -- pacing (global keys: send_delay_min_sec / send_delay_max_sec)
    send_delay_min_sec      INTEGER,
    send_delay_max_sec      INTEGER,
    -- concurrency (global key: concurrency_per_user_default)
    concurrency_per_user    INTEGER,
    -- resource protection (global keys introduced by Phase 5)
    max_campaign_size       INTEGER,
    max_queued_jobs         INTEGER,
    campaign_expiry_sec     INTEGER,

    updated_by              UUID,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT user_config_overrides_user_fk
        FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE,

    -- Each dial is NULL (inherit) or a positive value. Not a tuning constant: 0/negative are
    -- structurally meaningless for every column here, so this is a domain constraint (Rule 1
    -- is not engaged — nothing here governs runtime behavior by itself, the resolved value does).
    CONSTRAINT user_config_overrides_positive_check
        CHECK ((send_delay_min_sec   IS NULL OR send_delay_min_sec   >= 0)
           AND (send_delay_max_sec   IS NULL OR send_delay_max_sec   >= 0)
           AND (concurrency_per_user IS NULL OR concurrency_per_user >  0)
           AND (max_campaign_size    IS NULL OR max_campaign_size    >  0)
           AND (max_queued_jobs      IS NULL OR max_queued_jobs      >  0)
           AND (campaign_expiry_sec  IS NULL OR campaign_expiry_sec  >  0)),

    -- Intra-row pacing ordering only — the resolved-set case (M3-P1-OD-8) is Phase 6's to enforce
    -- at write time, not expressible in a row-level CHECK against a value in another table.
    CONSTRAINT user_config_overrides_delay_order_check
        CHECK (send_delay_min_sec IS NULL OR send_delay_max_sec IS NULL
               OR send_delay_min_sec <= send_delay_max_sec)
);

ALTER TABLE byosmtp.user_config_overrides OWNER TO byosmtp_owner;

GRANT SELECT, INSERT, UPDATE, DELETE ON byosmtp.user_config_overrides TO byosmtp_module;

ALTER TABLE byosmtp.user_config_overrides ENABLE ROW LEVEL SECURITY;
CREATE POLICY user_config_overrides_module_rw ON byosmtp.user_config_overrides
    FOR ALL TO byosmtp_module USING (true) WITH CHECK (true);

INSERT INTO byosmtp.schema_migrations (version) VALUES ('0030');
