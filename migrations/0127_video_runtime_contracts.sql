-- Explicit compatibility classification for pre-versioned official Ark rows.
-- These defaults support old binaries that omit new columns. New writers must
-- send the exact contract explicitly. Empty strings are not normalized to v1.
-- No task status, evidence, price, wallet, reservation or journal is rewritten.
-- Each table's columns are one atomic ALTER. MySQL commits between the four
-- ALTERs, so the migration runner must finish/replay before starting new code.
-- The version columns use NO PAD binary collation: SQL candidates must reject
-- case changes and trailing spaces exactly as the Go contract resolver does.
ALTER TABLE video_task_states
 ADD COLUMN runtime_version VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL DEFAULT 'ark-official-legacy-v1',
 ADD COLUMN adapter_version VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL DEFAULT 'ark-v3-v1',
 ADD COLUMN protocol_contract_version VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL DEFAULT 'video-contract-v1',
 ADD COLUMN billing_schema_version VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL DEFAULT 'usd-decimal-v1',
 ADD KEY idx_video_runtime_poll(runtime_version,adapter_version,protocol_contract_version,billing_schema_version,deleted_at,next_poll_at,task_id),
 ADD KEY idx_video_runtime_pending_usage(billing_state,task_id,org_id);

ALTER TABLE video_billing_snapshots
 ADD COLUMN billing_schema_version VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL DEFAULT 'usd-decimal-v1';

ALTER TABLE video_operations
 ADD COLUMN runtime_version VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL DEFAULT 'ark-official-legacy-v1',
 ADD COLUMN adapter_version VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL DEFAULT 'ark-v3-v1',
 ADD COLUMN protocol_contract_version VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL DEFAULT 'video-contract-v1',
 ADD KEY idx_video_runtime_operation(runtime_version,adapter_version,protocol_contract_version,status,created_at,id);

ALTER TABLE video_query_receipts
 ADD COLUMN runtime_version VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL DEFAULT 'ark-official-legacy-v1',
 ADD COLUMN adapter_version VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL DEFAULT 'ark-v3-v1',
 ADD COLUMN protocol_contract_version VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL DEFAULT 'video-contract-v1',
 ADD COLUMN observation_schema_version INT UNSIGNED NOT NULL DEFAULT 1,
 ADD KEY idx_video_runtime_receipt(runtime_version,adapter_version,protocol_contract_version,observation_schema_version,applied_at,received_at,attempt_id);
