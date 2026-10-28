-- Expand only. Each table's additions are one atomic ALTER. Both ALTERs must
-- finish before the consumer starts; interrupted replay records neither half
-- as a complete migration. Existing columns, defaults, indexes and rows stay.
ALTER TABLE video_task_states
 ADD COLUMN settlement_receipt_id VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL DEFAULT NULL;

ALTER TABLE video_query_receipts
 ADD COLUMN generation_applied_at DATETIME(3) NULL DEFAULT NULL,
 ADD COLUMN settlement_kind VARCHAR(24) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL DEFAULT '',
 ADD COLUMN settlement_evidence_sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
 ADD KEY idx_video_query_phase_due
  (runtime_version,adapter_version,protocol_contract_version,
   observation_schema_version,settlement_kind,applied_at,
   recovery_due_at,received_at,attempt_id),
 ADD KEY idx_video_query_task_financial_conflict
  (org_id,task_id,apply_error_code,attempt_id);
