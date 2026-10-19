-- Portable immutable price snapshots, never live channel rules or account data.
CREATE TABLE video_pricing_templates (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 version VARCHAR(64) NOT NULL,
 model VARCHAR(128) NOT NULL,
 body_hash CHAR(64) NOT NULL,
 body_json LONGTEXT NOT NULL,
 created_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_pricing_template(version,model)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
