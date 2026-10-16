CREATE TABLE ledger_export_jobs (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, user_id BIGINT UNSIGNED NOT NULL,
 idempotency_key VARCHAR(128) NOT NULL, request_hash CHAR(64) NOT NULL,
 filter_json TEXT NOT NULL, max_movement_id BIGINT UNSIGNED NOT NULL,
 cursor_id BIGINT UNSIGNED NOT NULL DEFAULT 0, row_count INT NOT NULL DEFAULT 0,
 status VARCHAR(16) NOT NULL, error_code VARCHAR(64) NOT NULL DEFAULT '',
 csv_data MEDIUMTEXT NOT NULL, created_at DATETIME(3) NOT NULL,
 expires_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_fund_export_request (org_id,user_id,idempotency_key),
 KEY idx_fund_export_work (status,expires_at,id),
 KEY idx_fund_export_owner (org_id,user_id,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
