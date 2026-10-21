-- Ark S2: durable video_provider_bill_heads
CREATE TABLE video_provider_bill_heads (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 provider_scope_id BIGINT UNSIGNED NOT NULL, source VARCHAR(64) NOT NULL,
 source_record_id VARCHAR(128) NOT NULL, source_revision BIGINT NOT NULL,
 line_id BIGINT UNSIGNED NOT NULL,
 UNIQUE KEY uk_video_bill_head(provider_scope_id,source,source_record_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
