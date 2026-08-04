-- send_byo_0046_visibility.down.sql — reverse of 0046's own content only. The notifications table
-- and its two enums are 0033/0025's (M3-P7-OD-9), reversed by THEIR down files, not this one.

DELETE FROM send_byo.config WHERE key IN (
    'notification_digest_window_sec',
    'notification_dedup_window_sec',
    'notification_immediate_severities',
    'notifications_enabled',
    'notification_tick_interval_sec',
    'destination_throttle_window_sec',
    'destination_throttle_user_threshold',
    'quote_capacity_projection_enabled',
    'quote_long_duration_warn_sec',
    'score_alert_threshold'
);

-- Restore the trigger function to 0022's body VERBATIM (not 0008's) — 0022's fix (gating error
-- context behind fn_job_event_is_error_transition) must survive this reversal exactly as it must
-- survive the up file's own replacement. send_byo_0022.down.sql is the model for this reversal.
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
        account_id, box_id, error_class, error_message, metadata
    ) VALUES (
        NEW.id, NEW.user_id, OLD.state, NEW.state, v_actor::send_byo.event_actor,
        NEW.assigned_account_id, NEW.assigned_box_id,
        CASE WHEN v_is_error THEN NEW.last_error_class   ELSE NULL END,
        CASE WHEN v_is_error THEN NEW.last_error_message ELSE NULL END,
        NULL
    );
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
ALTER FUNCTION send_byo.fn_job_events_on_state_change() OWNER TO send_byo_owner;

DROP INDEX IF EXISTS send_byo.job_events_destination_domain_throttle_idx;
ALTER TABLE send_byo.job_events DROP COLUMN IF EXISTS destination_domain;

ALTER TABLE send_byo.jobs DROP CONSTRAINT IF EXISTS jobs_last_smtp_response_len_check;
ALTER TABLE send_byo.jobs DROP COLUMN IF EXISTS last_smtp_response;

DELETE FROM send_byo.schema_migrations WHERE version = '0046';
