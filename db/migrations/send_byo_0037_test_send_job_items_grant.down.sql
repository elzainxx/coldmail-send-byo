-- send_byo_0037_test_send_job_items_grant.down.sql — revoke the narrow grant.

REVOKE INSERT ON public.job_items FROM send_byo_app;

DELETE FROM send_byo.schema_migrations WHERE version = '0037';
