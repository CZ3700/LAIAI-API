-- Ark console pricing evidence and auditable fund activity.
CREATE TABLE video_pricing_publications (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 model VARCHAR(64) NOT NULL, version VARCHAR(64) NOT NULL,
 config_version VARCHAR(64) NOT NULL, idempotency_key VARCHAR(128) NOT NULL,
 body_hash CHAR(64) NOT NULL, snapshot_json MEDIUMTEXT NOT NULL,
 created_by BIGINT UNSIGNED NOT NULL, created_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_pricing_publication (model,version),
 UNIQUE KEY uk_video_pricing_request (created_by,idempotency_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
