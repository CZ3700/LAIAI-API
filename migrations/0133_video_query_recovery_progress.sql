-- Expand only. The UTC expression also serves old writers that omit both
-- columns; explicit NULL is the new automatic-recovery review barrier.
-- Keep both old indexes for N-1 binaries. One atomic ALTER is replayable after
-- its implicit commit and before schema_migrations records this version.
ALTER TABLE video_query_receipts
 ADD COLUMN recovery_due_at DATETIME(3) NULL DEFAULT (UTC_TIMESTAMP(3)),
 ADD COLUMN recovery_version BIGINT UNSIGNED NOT NULL DEFAULT 0,
 ADD KEY idx_video_query_recovery_due
  (runtime_version,adapter_version,protocol_contract_version,
   observation_schema_version,applied_at,recovery_due_at,received_at,attempt_id);

-- This touches only the new column and preserves all original evidence.
-- A repeated migration must not overwrite any progress already owned by N.
UPDATE video_query_receipts
 SET recovery_due_at = NULL
 WHERE applied_at IS NULL AND recovery_version = 0
  AND recovery_due_at IS NOT NULL
  AND CAST(apply_error_code AS BINARY) = CAST(_utf8mb4'settlement_conflict' AS BINARY);
