-- Expand only. Derived flags preserve the existing exact purpose partition
-- and PAD SPACE capture-status comparison. Old writers omit these columns.
-- Keep the old indexes for old binaries. One atomic ALTER supports replay
-- after DDL commit but before schema_migrations records this version.
ALTER TABLE invocation_payloads
 ADD COLUMN purge_is_execution TINYINT UNSIGNED
  GENERATED ALWAYS AS
   (CAST(purpose AS BINARY) = CAST(_utf8mb4'execution' AS BINARY)) VIRTUAL,
 ADD COLUMN purge_needs_erasure TINYINT UNSIGNED
  GENERATED ALWAYS AS
   (capture_status <> _utf8mb4'expired' COLLATE utf8mb4_bin) VIRTUAL,
 ADD KEY idx_video_payload_purge_due
  (purge_is_execution,purge_needs_erasure,expires_at,payload_id,org_id),
 ADD KEY idx_video_payload_cleanup_partition
  (purge_is_execution,capture_status,object_deleted_at,cleanup_after,payload_id,org_id);
