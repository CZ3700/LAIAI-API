-- Ark S2: durable video_provider_bill_imports
CREATE TABLE video_provider_bill_imports (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 import_key VARCHAR(64) NOT NULL, provider_scope_id BIGINT UNSIGNED NOT NULL,
 source VARCHAR(64) NOT NULL, source_version VARCHAR(64) NOT NULL,
 source_hash CHAR(64) NOT NULL, payload_ref VARCHAR(128) NOT NULL,
 expected_rows BIGINT NOT NULL, state VARCHAR(16) NOT NULL,
 created_at DATETIME(3) NOT NULL, completed_at DATETIME(3) NULL,
 UNIQUE KEY uk_video_bill_import(provider_scope_id,import_key),
 KEY idx_video_bill_import_progress(state,created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
