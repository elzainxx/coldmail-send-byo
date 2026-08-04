-- byosmtp_0032_create_campaigns.up.sql — Module 3 Phase 1 §4.3, G3. Campaign expiry with a
-- suspension-pausable clock. New table (nothing existing to add columns to).
--
-- Clock-pause mechanism: expires_at is the AUTHORITY, pushed FORWARD on reinstate (not compared
-- against paused_total) — keeps the sweep predicate a plain indexable comparison on one column
-- rather than an expression over two that no partial index can serve (the difference between an
-- index scan and a full scan on the one query that runs on every sweep tick). Phase 5 §7 supplies
-- the suspend/reinstate writes; this migration only owns the shape.
--
-- M3-P1-OD-4, RULED YES: jobs.campaign_id gains an FK to this table, ON DELETE RESTRICT (not SET
-- NULL, deliberately — no maintenance UPDATE against jobs, no interaction with jobs' AFTER UPDATE
-- OF state trigger or Rule 9's lock ordering). Rule-9-safe: campaign_id never changes after
-- insert, so an UPDATE leaving that FK key unchanged takes no KEY SHARE on the referenced row.
--
-- THE FK ITSELF IS DEFERRED TO PHASE 5, NOT ADDED HERE — flagged, not silently applied. The
-- ruling's own insert-ordering argument ("/v1/run upserts the campaign row in the same
-- transaction, Phase 5 §5.3") is the thing that makes the FK safe, and that upsert is Phase 5's
-- code, not G3's. platformapi/store.go's current INSERT INTO byosmtp.jobs writes no campaigns row
-- at all; adding a RESTRICT FK now would make every existing job-creation path (store.go, and
-- every harness fixture that inserts a jobs row directly) fail its very next INSERT with 23503,
-- breaking the green build for a consumer that does not exist yet. G3's own exit criteria
-- (Docs/history/phase-2-strip-shared-pool.md §8.1 items 12-19) never list this FK — only Phase 1 §4.3's
-- DDL commentary does. Add it in the same Phase 5 commit that ships the upsert.

CREATE TABLE byosmtp.campaigns (
    id                  UUID PRIMARY KEY,      -- the campaign_id the platform already sends on /v1/run
    user_id             UUID NOT NULL,
    started_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at          TIMESTAMPTZ NOT NULL,
    clock_paused_at     TIMESTAMPTZ,
    paused_total_sec    BIGINT NOT NULL DEFAULT 0,
    expired_at          TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT campaigns_user_fk
        FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE,
    CONSTRAINT campaigns_expires_after_start_check CHECK (expires_at > started_at),
    CONSTRAINT campaigns_paused_total_check        CHECK (paused_total_sec >= 0),
    CONSTRAINT campaigns_expired_valid_check
        CHECK (expired_at IS NULL OR expired_at >= started_at)
);

ALTER TABLE byosmtp.campaigns OWNER TO byosmtp_owner;

-- The expiry sweep's claim predicate. Excluding clock_paused_at IS NOT NULL in the INDEX (not only
-- in the query) is what makes "suspension pauses the clock" a schema-level property: a suspended
-- campaign is not merely skipped, it is not in the sweep's index at all.
CREATE INDEX campaigns_expiry_sweep_idx ON byosmtp.campaigns (expires_at)
    WHERE expired_at IS NULL AND clock_paused_at IS NULL;

-- One active campaign per user is enforced PROCEDURALLY (advisory lock in platformapi/store.go)
-- and stays that way; this index serves the lookup, it does not replace the lock.
CREATE INDEX campaigns_user_id_idx ON byosmtp.campaigns (user_id);

GRANT SELECT, INSERT, UPDATE, DELETE ON byosmtp.campaigns TO byosmtp_module;

ALTER TABLE byosmtp.campaigns ENABLE ROW LEVEL SECURITY;
CREATE POLICY campaigns_module_rw ON byosmtp.campaigns
    FOR ALL TO byosmtp_module USING (true) WITH CHECK (true);

INSERT INTO byosmtp.schema_migrations (version) VALUES ('0032');
