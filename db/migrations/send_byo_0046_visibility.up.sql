-- send_byo_0046_visibility.up.sql — Module 3 Phase 7. The notification objects (notifications
-- table, notification_severity/notification_state enums) already landed in 0025/0033
-- (M3-P7-OD-9 RULED (b) 2026-07-31: Phase 7's shape is authoritative, but Phase 1's 0033 stays
-- the single create site). This migration is Phase 7's OWN content only: three ALTERs, two
-- indexes, one function replacement, and ten seeded config keys. No table, no type.

-- §1.4: provider text WITHOUT the internal slug jobs.last_error_message concatenates
-- (rep.ErrorReason + ": " + rep.SMTPResponse) — this is the slug-free column a user-facing
-- surface reads instead. 2000 chars mirrors jobs.last_error_message's existing bound and Phase
-- 3's accounts_credential_verify_response_len_check — one bound for provider text across the
-- schema.
ALTER TABLE send_byo.jobs
    ADD COLUMN last_smtp_response TEXT,
    ADD CONSTRAINT jobs_last_smtp_response_len_check
        CHECK (last_smtp_response IS NULL OR char_length(last_smtp_response) <= 2000);

-- §7 (M3-P7-OD-6): the domain the throttle was observed against, denormalized deliberately so
-- the aggregation does not depend on send_logs' retention window (the safeguard chosen INSTEAD
-- of a per-destination cap must not hinge on a debug-retention setting). Nullable, so every
-- inherited row and every non-throttle row stays valid (Rule 5).
ALTER TABLE send_byo.job_events ADD COLUMN destination_domain TEXT;
CREATE INDEX job_events_destination_domain_throttle_idx
    ON send_byo.job_events (destination_domain, created_at)
    WHERE error_class = 'throttle';

-- ...and the writer, without which the column above is NULL forever (§7). job_events is written
-- ONLY by this trigger function, which names its columns explicitly.
--
-- THE BASE BODY IS send_byo_0022's, NOT send_byo_0008's. 0008 creates this function; 0022
-- REPLACES it to gate error context behind fn_job_event_is_error_transition (it fixed a live 4x
-- inflation of GET /v1/admin/health's errors_last_window by stamping stale last_error_* onto
-- successes). Writing this CREATE OR REPLACE from 0008's body would SILENTLY REVERT that fix — a
-- data-correctness regression with no migration_lint signal, because replacing a function is
-- always syntactically valid. Every line below except the destination_domain column/value is
-- 0022 verbatim: the early return on OLD.state = NEW.state (CLAUDE.md Rule 9(a) depends on it),
-- the strict actor RAISE, and the two CASE-gated error columns.
CREATE OR REPLACE FUNCTION send_byo.fn_job_events_on_state_change()
RETURNS TRIGGER AS $$
DECLARE
    v_actor TEXT;
    v_is_error BOOLEAN;
BEGIN
    IF OLD.state = NEW.state THEN
        RETURN NEW; -- no state change, no event
    END IF;

    v_actor := current_setting('send_byo.actor', true);
    IF v_actor IS NULL OR v_actor = '' THEN
        RAISE EXCEPTION 'send_byo.actor GUC not set: cannot record state transition'
            USING HINT = 'Call set_config(''send_byo.actor'', ''dispatcher'', true) before UPDATE';
    END IF;

    v_is_error := send_byo.fn_job_event_is_error_transition(OLD.state, NEW.state);

    INSERT INTO send_byo.job_events (
        job_id, user_id, from_state, to_state, actor,
        account_id, box_id, error_class, error_message, metadata,
        destination_domain
    ) VALUES (
        NEW.id, NEW.user_id, OLD.state, NEW.state, v_actor::send_byo.event_actor,
        NEW.assigned_account_id, NEW.assigned_box_id,
        CASE WHEN v_is_error THEN NEW.last_error_class   ELSE NULL END,
        CASE WHEN v_is_error THEN NEW.last_error_message ELSE NULL END,
        NULL,
        split_part(NEW.recipient_email, '@', 2)
    );
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
ALTER FUNCTION send_byo.fn_job_events_on_state_change() OWNER TO send_byo_owner;
-- No CREATE TRIGGER: the inherited trigger already points at this function by name.

-- §8: ten seeded config keys, each naming its consumer at seeding (Rule 1; mandatory check 2).
-- All global-only — none is per-user overridable (§8's own "why none" note: these govern the
-- module's outbound behavior, not a user's resource allocation).
INSERT INTO send_byo.config (key, value, default_value, description, value_type) VALUES
('notification_digest_window_sec', '{"v":3600}', '{"v":3600}',
 'Batching window for digest-tier notifications (§3.2): failures inside this window for one user
collapse into ONE delivered notification. Not per-user (M3-P7-OD-7) — a product/UI concern, not a
config dial.', 'integer'),
('notification_dedup_window_sec', '{"v":86400}', '{"v":86400}',
 'An ongoing condition (still open, same dedup_key) re-notifies only after this many seconds since
last_seen_at advanced with no fresh notification (§3.3). Persisted dedup, not module 2''s
in-memory tracker.', 'integer'),
('notification_immediate_severities', '{"v":["critical"]}', '{"v":["critical"]}',
 'Severities that bypass the digest window and deliver immediately (§3.1/§3.2). A config dial, not
a hardcoded tier map — Rule 1 forbids a literal governing which severities are urgent.', 'array<string>'),
('notifications_enabled', '{"v":true}', '{"v":true}',
 'Kill switch (Rule 7) for the notification delivery loop (§3.2). false stops delivery cleanly;
already-persisted rows are untouched.', 'boolean'),
('notification_tick_interval_sec', '{"v":60}', '{"v":60}',
 'The notification delivery loop''s own ticker period (§3.2) — a singleton-loop property, not a
per-user or per-condition dial.', 'integer'),
('destination_throttle_window_sec', '{"v":3600}', '{"v":3600}',
 'Trailing window (§7) the operator health endpoint''s per-destination throttle-concentration
aggregation covers. Reads job_events.destination_domain, never send_logs (M3-P7-OD-6) — the
aggregation must not depend on debug retention.', 'integer'),
('destination_throttle_user_threshold', '{"v":3}', '{"v":3}',
 'Distinct-users-affected count (§7) at or above which a destination_throttles condition fires for
one destination domain within destination_throttle_window_sec.', 'integer'),
('quote_capacity_projection_enabled', '{"v":true}', '{"v":true}',
 'Whether POST /v1/quote (§4.2) includes the capacity/duration-projection block at all. false omits
the block entirely rather than returning a degraded one.', 'boolean'),
('quote_long_duration_warn_sec', '{"v":172800}', '{"v":172800}',
 'A quote''s estimated_duration_sec at or above this many seconds (default 48h) adds a
long_duration entry to warnings[] (§4.3) — a warning threshold, not a resource allocation, so not
per-user overridable.', 'integer'),
('score_alert_threshold', '{"v":30}', '{"v":30}',
 'account_status_score at or below which §3.1 raises a score-warning digest event for that
account. Replaces quarantine_threshold (dropped Phase 5, M3-P5-OD-1''s split ruling) under a name
that does not lie about what it does — this key warns, it does not quarantine. Inherits the old
default (30); only the name and the missing consumer were the problem.', 'integer');

INSERT INTO send_byo.schema_migrations (version) VALUES ('0046');
