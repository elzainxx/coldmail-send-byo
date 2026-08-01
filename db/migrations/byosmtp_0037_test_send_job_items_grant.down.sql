-- byosmtp_0037_test_send_job_items_grant.down.sql — revoke the narrow grant.

REVOKE INSERT ON public.job_items FROM byosmtp_module;

DELETE FROM byosmtp.schema_migrations WHERE version = '0037';
