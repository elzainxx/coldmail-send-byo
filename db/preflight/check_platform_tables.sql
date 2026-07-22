-- Pre-flight external-table existence check (OD-8 / ADR-0007).
-- Runs BEFORE the migration chain, in CI and again at deploy time. Checks EXISTENCE only
-- (not column shape — a shape check would break whenever the platform legitimately adds a
-- column). Fails fast and loud so an FK-bearing migration never dies with an opaque
-- "relation public.job_items does not exist".
--
-- Assumed shapes (platform-owned, confirm before Phase 1 live — see ADR-0007):
--   public.users(id uuid pk, email, is_active, created_at)
--   public.job_items(id uuid pk, campaign_id, user_id, recipient_email,
--                    rendered_subject, rendered_body, reply_to, status, created_at)
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_tables WHERE schemaname='public' AND tablename='users') THEN
        RAISE EXCEPTION 'Preflight failed: public.users does not exist. Deploy the platform schema first.';
    END IF;
    IF NOT EXISTS (SELECT FROM pg_tables WHERE schemaname='public' AND tablename='job_items') THEN
        RAISE EXCEPTION 'Preflight failed: public.job_items does not exist. Deploy the platform schema first.';
    END IF;
END $$;
