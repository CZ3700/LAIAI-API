-- Ark console pricing evidence and auditable fund activity.
CREATE TABLE ledger_operations (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, idempotency_key VARCHAR(160) NOT NULL,
 request_hash VARCHAR(64) NOT NULL DEFAULT '', type VARCHAR(64) NOT NULL,
 direction VARCHAR(16) NOT NULL, requested_amount DECIMAL(20,8) NOT NULL,
 currency CHAR(3) NOT NULL, status VARCHAR(16) NOT NULL,
 ref_type VARCHAR(64) NOT NULL, ref_id VARCHAR(160) NOT NULL,
 failure_code VARCHAR(64) NOT NULL DEFAULT '', created_at DATETIME(3) NOT NULL,
 updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_ledger_operation (org_id,idempotency_key),
 KEY idx_ledger_operation_org (org_id,id),
 KEY idx_ledger_operation_status (org_id,status,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
