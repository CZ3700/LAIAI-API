CREATE TABLE invocation_export_admission (
 id TINYINT UNSIGNED NOT NULL PRIMARY KEY
) ENGINE=InnoDB;
INSERT INTO invocation_export_admission (id) VALUES (1) ON DUPLICATE KEY UPDATE id = 1;

CREATE TABLE invocation_export_jobs (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL,
 user_id BIGINT UNSIGNED NOT NULL,
 request_key CHAR(36) NOT NULL,
 filter_hash VARCHAR(71) NOT NULL,
 spec_json TEXT NOT NULL,
 max_log_id BIGINT UNSIGNED NOT NULL,
 slice_until DATETIME(3) NOT NULL,
 cursor_time DATETIME(3) NULL,
 cursor_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
 row_count BIGINT NOT NULL DEFAULT 0,
 byte_count BIGINT NOT NULL DEFAULT 0,
 chunk_count INT NOT NULL DEFAULT 0,
 status VARCHAR(16) NOT NULL,
 error_code VARCHAR(32) NOT NULL DEFAULT '',
 created_at DATETIME(3) NOT NULL,
 updated_at DATETIME(3) NOT NULL,
 expires_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_inv_export_request (org_id,user_id,request_key),
 KEY idx_inv_export_owner (org_id,user_id,id),
 KEY idx_inv_export_expiry (expires_at,id),
 KEY idx_inv_export_worker (status,updated_at,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE invocation_export_chunks (
 job_id BIGINT UNSIGNED NOT NULL,
 seq INT NOT NULL,
 body MEDIUMBLOB NOT NULL,
 PRIMARY KEY (job_id,seq),
 CONSTRAINT fk_inv_export_chunk_job FOREIGN KEY (job_id) REFERENCES invocation_export_jobs (id) ON DELETE CASCADE
) ENGINE=InnoDB;
