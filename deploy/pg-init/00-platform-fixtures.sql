-- DEV FIXTURE ONLY — NOT the real platform schema (ADR-0007/OD-8).
-- The send_byo module reads two platform-owned tables (public.users, public.job_items) and
-- writes exactly one thing there: test-send's INSERT into public.job_items (six columns, send_byo_0037). It never updates or deletes either table and holds no DELETE. In prod these are owned and created by the PLATFORM. Here we seed
-- minimal fixtures of the ASSUMED shapes so the migration pre-flight (existence check)
-- passes locally and the worker/jobs FKs in Phase 1 have real targets.
--
-- Confirm the exact columns against the real platform schema before Phase 1 runs live
-- (standing action item — the pre-flight checks EXISTENCE only, not column shape).

CREATE TABLE IF NOT EXISTS public.users (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email      text,
    is_active  boolean DEFAULT true,
    created_at timestamptz DEFAULT now()
);

CREATE TABLE IF NOT EXISTS public.job_items (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    campaign_id      uuid,
    user_id          uuid,
    recipient_email  text,
    rendered_subject text,
    rendered_body    text,
    rendered_text    text,          -- the optional plain-text alternative (NULL = none); the PLATFORM adds this column (Phase 3 close-out C5)
    unsubscribe_url  text,          -- the per-recipient unsubscribe link (NULL = none); the PLATFORM adds this column (platform_0017, Phase 5 S4)
    reply_to         text,
    status           text,
    created_at       timestamptz DEFAULT now()
);
