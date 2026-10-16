-- Ark console pricing evidence and auditable fund activity.
CREATE TABLE video_minimum_rule_sets (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 model VARCHAR(64) NOT NULL, version VARCHAR(64) NOT NULL,
 source_url VARCHAR(1024) NOT NULL, evidence_hash CHAR(64) NOT NULL,
 observed_at DATETIME(3) NOT NULL, verified_by BIGINT UNSIGNED NOT NULL,
 verified_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_minimum_version (model,version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
