-- Expand only. Pre-action writers omit this column and retain NULL.
-- Match the existing upstream binding collation for indexed joins. Consumers
-- additionally compare OCTET_LENGTH and exact Go strings for PAD SPACE IDs.
ALTER TABLE video_execution_attempts
 ADD COLUMN target_upstream_task_id VARCHAR(128) COLLATE utf8mb4_bin NULL;
