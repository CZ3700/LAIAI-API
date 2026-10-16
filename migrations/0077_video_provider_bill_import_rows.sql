-- Ark S2: durable video_provider_bill_import_rows
CREATE TABLE video_provider_bill_import_rows (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 import_id BIGINT UNSIGNED NOT NULL, source_ordinal BIGINT NOT NULL,
 line_id BIGINT UNSIGNED NOT NULL, content_hash CHAR(64) NOT NULL,
 UNIQUE KEY uk_video_bill_import_row(import_id,source_ordinal),
 KEY idx_video_bill_receipt_line(line_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
