-- Ark S2: durable video_provider_bill_lines
CREATE TABLE video_provider_bill_lines (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 provider_scope_id BIGINT UNSIGNED NOT NULL, source VARCHAR(64) NOT NULL,
 source_record_id VARCHAR(128) NOT NULL, source_version VARCHAR(64) NOT NULL,
 source_revision BIGINT NOT NULL, content_hash CHAR(64) NOT NULL,
 evidence_hash CHAR(64) NOT NULL, payload_ref VARCHAR(128) NOT NULL,
 granularity VARCHAR(16) NOT NULL, upstream_task_id VARCHAR(128) NOT NULL,
 model VARCHAR(64) NOT NULL, period_start DATETIME(3) NOT NULL,
 period_end DATETIME(3) NOT NULL, completion_tokens BIGINT UNSIGNED NULL,
 amount_cny DECIMAL(20,8) NOT NULL, observed_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_bill_version(provider_scope_id,source,source_record_id,source_version),
 UNIQUE KEY uk_video_bill_revision(provider_scope_id,source,source_record_id,source_revision),
 KEY idx_video_bill_upstream(provider_scope_id,upstream_task_id),
 KEY idx_video_bill_period(provider_scope_id,period_start)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
