-- sender_0003_create_enums.up.sql — Phase 1: all native enum types (LOCKED, additive-only).
-- Enums are created before any table (dependency order). Each is owned by byosmtp_owner (the
-- schema owner, ADR-0004) so future ALTER TYPE ... ADD VALUE and object ownership are
-- consistent. Values are additive-only FOREVER (CLAUDE.md Rule 5): new values may be appended
-- via ALTER TYPE ... ADD VALUE, but existing values are never renamed or removed.

CREATE TYPE byosmtp.job_state AS ENUM (
    'queued',      -- Available for assignment
    'assigned',    -- Claimed by dispatcher, not yet sent to box
    'sending',     -- Handed off to box, in-flight
    'sent',        -- SMTP-accepted (250 OK), not delivery-confirmed
    'failed',      -- Terminal failure (all eligible accounts exhausted)
    'bounced'      -- Terminal bounce (550 no-such-user or equivalent)
);

CREATE TYPE byosmtp.error_class AS ENUM (
    'bounce_permanent',    -- 550/no-such-user, job -> bounced
    'transport_4xx',       -- 4xx transport error, retry then requeue
    'auth_failure',        -- SMTP auth failed, requeue + quarantine account
    'throttle',            -- Rate limit or daily limit hit, requeue + pause account
    'timeout',             -- Job lease expired or box heartbeat missed
    'unknown'              -- Catch-all for unclassified errors
);

CREATE TYPE byosmtp.pool_kind AS ENUM (
    'shared',      -- Shared pool, available to all users
    'dedicated'    -- Dedicated pool, owned by a specific user
);

CREATE TYPE byosmtp.routing_strategy AS ENUM (
    'random',       -- Pick random account from pool
    'round_robin',  -- Cycle through accounts in order
    'one_by_one'    -- Use one account until exhausted, then next
);

CREATE TYPE byosmtp.box_status AS ENUM (
    'active',      -- Heartbeat within threshold
    'stale',       -- Missed heartbeats, jobs being reclaimed
    'disabled'     -- Manually disabled by admin
);

CREATE TYPE byosmtp.event_actor AS ENUM (
    'dispatcher',   -- Dispatcher claimed job (queued -> assigned)
    'box',          -- Box reported outcome (sending -> sent/failed/bounced)
    'reclaim_loop', -- Reclaim loop requeued job (assigned/sending -> queued)
    'admin',        -- Admin manual intervention (any state -> any state)
    'system'        -- System-triggered transition (e.g. config change, migration)
);

CREATE TYPE byosmtp.pause_reason AS ENUM (
    'daily_limit',  -- Paused because the account hit its daily send limit
    'throttle',     -- Paused by Phase 5 rate-limiting logic (own timer, own clear)
    'manual'        -- Paused by admin action (clears only by admin action)
);

-- Own all enums as byosmtp_owner (the schema owner), not the admin migration role.
ALTER TYPE byosmtp.job_state       OWNER TO byosmtp_owner;
ALTER TYPE byosmtp.error_class     OWNER TO byosmtp_owner;
ALTER TYPE byosmtp.pool_kind       OWNER TO byosmtp_owner;
ALTER TYPE byosmtp.routing_strategy OWNER TO byosmtp_owner;
ALTER TYPE byosmtp.box_status      OWNER TO byosmtp_owner;
ALTER TYPE byosmtp.event_actor     OWNER TO byosmtp_owner;
ALTER TYPE byosmtp.pause_reason    OWNER TO byosmtp_owner;

-- Self-record.
INSERT INTO byosmtp.schema_migrations (version) VALUES ('0003');
