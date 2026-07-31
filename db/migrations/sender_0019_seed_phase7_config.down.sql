-- sender_0019_seed_phase7_config.down.sql — reverse of the Phase 7 config seed. Deletes exactly the
-- six keys sender_0019 inserted and un-records the version. Additive-only + reversible (DoD).
DELETE FROM byosmtp.config WHERE key IN
 ('startup_reconnect_window_sec','shutdown_drain_timeout_sec','retention_sweep_interval_ms',
  'retention_delete_batch_size','rollup_granularity','box_outcome_buffer_ttl_sec');
DELETE FROM byosmtp.schema_migrations WHERE version = '0019';
