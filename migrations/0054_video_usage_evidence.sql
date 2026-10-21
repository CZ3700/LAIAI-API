-- Ark video: durable video_usage_evidence
CREATE TABLE video_usage_evidence (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, task_id VARCHAR(64) NOT NULL,
 evidence_id VARCHAR(64) NOT NULL, source VARCHAR(32) NOT NULL,
 source_record_id VARCHAR(128) NOT NULL, source_version VARCHAR(64) NOT NULL,
 completion_tokens BIGINT UNSIGNED NULL, payload_ref VARCHAR(128) NOT NULL,
 observed_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_evidence_id(evidence_id),
 UNIQUE KEY uk_video_evidence_source(org_id,task_id,source,source_record_id,source_version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
